package codex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/runner"
)

type Provider struct {
	binary   string
	accounts []config.CodexAccountConfig
}

func New(binary string, accounts []config.CodexAccountConfig) *Provider {
	if binary == "" {
		binary = "codex"
	}
	return &Provider{binary: binary, accounts: accounts}
}

func (p *Provider) ID() string   { return "codex" }
func (p *Provider) Name() string { return "OpenAI Codex" }

func (p *Provider) Discover(_ context.Context) ([]domain.AccountCandidate, error) {
	if _, err := runner.LookPath(p.binary); err != nil {
		return nil, &domain.CodedError{Code: "codex_not_found", Err: errors.New("codex executable not found")}
	}
	accounts := append([]config.CodexAccountConfig(nil), p.accounts...)
	if len(accounts) == 0 {
		accounts = []config.CodexAccountConfig{{Label: "Codex", Home: config.DefaultCodexHome()}}
	}
	result := make([]domain.AccountCandidate, 0, len(accounts))
	for index, item := range accounts {
		home := config.ExpandPath(item.Home)
		if home == "" {
			continue
		}
		label := strings.TrimSpace(item.Label)
		if label == "" {
			label = fmt.Sprintf("Codex account %d", index+1)
		}
		sum := sha256.Sum256([]byte(filepath.Clean(home)))
		authPresent := "false"
		if info, err := os.Stat(filepath.Join(home, "auth.json")); err == nil && info.Mode().IsRegular() {
			authPresent = "true"
		}
		result = append(result, domain.AccountCandidate{
			ID: "codex:home:" + hex.EncodeToString(sum[:6]), ProviderID: "codex", Label: label,
			Source: "CODEX_HOME", Ref: home,
			SourceMeta: map[string]string{"home": home, "authPresent": authPresent},
		})
	}
	return result, nil
}

func (p *Provider) Fetch(ctx context.Context, candidate domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	accountResult, limitsResult, err := p.rpc(ctx, candidate.Ref)
	if err != nil {
		return domain.Account{}, domain.Snapshot{}, err
	}
	account, snapshot, err := Parse(accountResult, limitsResult, candidate)
	if err != nil {
		return account, snapshot, err
	}
	return account, snapshot, nil
}

func Parse(accountJSON, limitsJSON []byte, candidate domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	var accountPayload struct {
		Account *struct {
			Type     string  `json:"type"`
			Email    *string `json:"email"`
			PlanType string  `json:"planType"`
		} `json:"account"`
		RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
	}
	if err := json.Unmarshal(accountJSON, &accountPayload); err != nil {
		return domain.Account{}, domain.Snapshot{}, &domain.CodedError{Code: "invalid_account_response", Err: errors.New("invalid Codex account response")}
	}
	account := domain.Account{
		ID: candidate.ID, ProviderID: "codex", Label: candidate.Label,
		Source: candidate.Source, SourceMeta: cloneMeta(candidate.SourceMeta),
	}
	if accountPayload.Account != nil {
		account.Plan = accountPayload.Account.PlanType
		if account.Label == "" && accountPayload.Account.Email != nil {
			account.Label = *accountPayload.Account.Email
		}
	}
	// requiresOpenaiAuth describes whether this Codex build uses OpenAI
	// authentication; it is not a "logged out" flag. An authenticated ChatGPT
	// account legitimately returns requiresOpenaiAuth=true together with a
	// populated account. The absence of the account is the actionable signal.
	if accountPayload.Account == nil {
		return account, domain.Snapshot{}, &domain.CodedError{Code: "codex_auth_required", Err: errors.New("Codex CLI needs authentication for this CODEX_HOME")}
	}
	var limits getRateLimitsResponse
	if err := json.Unmarshal(limitsJSON, &limits); err != nil {
		return account, domain.Snapshot{}, &domain.CodedError{Code: "invalid_limits_response", Err: errors.New("invalid Codex rate-limit response")}
	}
	windows := make([]domain.QuotaWindow, 0)
	if len(limits.RateLimitsByLimitID) > 0 {
		limitIDs := make([]string, 0, len(limits.RateLimitsByLimitID))
		for limitID := range limits.RateLimitsByLimitID {
			limitIDs = append(limitIDs, limitID)
		}
		sort.Strings(limitIDs)
		for _, limitID := range limitIDs {
			snapshot := limits.RateLimitsByLimitID[limitID]
			windows = append(windows, rateLimitWindows(limitID, snapshot)...)
			if account.Plan == "" {
				account.Plan = snapshot.PlanType
			}
		}
	} else {
		windows = append(windows, rateLimitWindows("codex", limits.RateLimits)...)
		if account.Plan == "" {
			account.Plan = limits.RateLimits.PlanType
		}
	}
	if limits.RateLimits.Credits != nil && limits.RateLimits.Credits.Balance != nil {
		if balance, err := strconv.ParseFloat(*limits.RateLimits.Credits.Balance, 64); err == nil {
			windows = append(windows, domain.QuotaWindow{
				ID: "credits", Label: "Credits", Kind: "credits", Remaining: &balance, Unit: "credits",
			})
		}
	}
	snapshot, err := domain.NormalizeSnapshot(domain.Snapshot{
		AccountID: account.ID, FetchedAt: time.Now().UTC(), Status: domain.StatusFresh, Windows: windows,
	})
	if err != nil {
		return account, snapshot, &domain.CodedError{Code: "invalid_limits", Err: err}
	}
	return account, snapshot, nil
}

func (p *Provider) rpc(ctx context.Context, home string) ([]byte, []byte, error) {
	childContext, cancel := context.WithCancel(ctx)
	defer cancel()
	binary, err := runner.LookPath(p.binary)
	if err != nil {
		return nil, nil, &domain.CodedError{Code: "codex_not_found", Err: errors.New("codex executable not found")}
	}
	command := exec.CommandContext(childContext, binary, "app-server", "--stdio")
	command.Env = envWith(runner.CommandEnvironment(), "CODEX_HOME", home)
	// npm launchers spawn the real Codex binary. Cancel that whole process
	// group so a surviving child cannot hold the RPC pipes open indefinitely.
	configureProcessGroup(command)
	command.WaitDelay = time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, nil, &domain.CodedError{Code: "codex_start_failed", Err: errors.New("open Codex stdin")}
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, &domain.CodedError{Code: "codex_start_failed", Err: errors.New("open Codex stdout")}
	}
	stderr := &syncBuffer{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, &domain.CodedError{Code: "codex_start_failed", Err: errors.New("start Codex app-server")}
	}
	// exec copies the child's stderr from a goroutine of its own, and only Wait
	// guarantees that copy is complete. Reading the buffer before then races the
	// copier and yields whatever happened to have arrived.
	var reaped sync.Once
	reap := func() {
		reaped.Do(func() {
			cancel()
			_ = stdin.Close()
			_ = stdout.Close()
			_ = command.Wait()
		})
	}
	defer reap()
	stderrTail := func() string {
		reap()
		return stderr.lastLine()
	}
	// Stdio reads/writes do not observe context cancellation themselves.
	// Close them explicitly even if a descendant escaped the process group.
	stopClosing := context.AfterFunc(childContext, func() {
		_ = stdin.Close()
		_ = stdout.Close()
	})
	defer stopClosing()
	encoder := json.NewEncoder(stdin)
	if err := encoder.Encode(map[string]any{
		"id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{"name": "quotadeck", "title": "QuotaDeck", "version": "0.1.0"}},
	}); err != nil {
		return nil, nil, rpcFailure(home, stderrTail, errors.New("write Codex initialize request"))
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	if _, err := responseByID(scanner, 1); err != nil {
		return nil, nil, rpcFailure(home, stderrTail, err)
	}
	for _, message := range []map[string]any{
		{"method": "initialized"},
		{"id": 2, "method": "account/read", "params": map[string]bool{"refreshToken": false}},
		{"id": 3, "method": "account/rateLimits/read", "params": nil},
	} {
		if err := encoder.Encode(message); err != nil {
			return nil, nil, rpcFailure(home, stderrTail, errors.New("write Codex request"))
		}
	}
	// Collect both answers before concluding. A per-request error must not end
	// the read: the other request carries the signal that names the cause, and
	// the app-server answers them in either order.
	var accountResult, limitsResult []byte
	var accountErr, limitsErr error
	for (accountResult == nil && accountErr == nil) || (limitsResult == nil && limitsErr == nil) {
		response, err := nextResponse(scanner)
		if err != nil {
			var rpc *rpcError
			if !errors.As(err, &rpc) {
				// The stream itself failed; nothing further can arrive.
				return nil, nil, rpcFailure(home, stderrTail, err)
			}
			switch response.ID {
			case 2:
				accountErr = err
			case 3:
				limitsErr = err
			default:
				return nil, nil, rpcFailure(home, stderrTail, err)
			}
			continue
		}
		switch response.ID {
		case 2:
			accountResult = response.Result
		case 3:
			limitsResult = response.Result
		}
	}
	_ = stdin.Close()
	if accountErr != nil {
		return nil, nil, rpcFailure(home, stderrTail, accountErr)
	}
	if limitsErr != nil {
		// A CODEX_HOME that was never signed in returns a null account and fails
		// the rate-limit read with wording of its own. The null account is the
		// structured signal, and it decides regardless of that wording.
		if accountAbsent(accountResult) {
			return nil, nil, &domain.CodedError{Code: "codex_auth_required", Err: fmt.Errorf(
				"Codex has no account for CODEX_HOME %s, run `codex login`", home)}
		}
		return nil, nil, rpcFailure(home, stderrTail, limitsErr)
	}
	return accountResult, limitsResult, nil
}

func accountAbsent(accountJSON []byte) bool {
	var payload struct {
		Account *json.RawMessage `json:"account"`
	}
	if err := json.Unmarshal(accountJSON, &payload); err != nil {
		return false
	}
	return payload.Account == nil || string(*payload.Account) == "null"
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func responseByID(scanner *bufio.Scanner, id int) (json.RawMessage, error) {
	for {
		response, err := nextResponse(scanner)
		if err != nil {
			return nil, err
		}
		if response.ID == id {
			return response.Result, nil
		}
	}
}

func nextResponse(scanner *bufio.Scanner) (rpcResponse, error) {
	for scanner.Scan() {
		var response rpcResponse
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			continue
		}
		if response.ID == 0 {
			continue
		}
		if response.Error != nil {
			return response, &rpcError{Code: response.Error.Code, Message: response.Error.Message}
		}
		return response, nil
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, fmt.Errorf("read Codex app-server response: %w", err)
	}
	return rpcResponse{}, errors.New("Codex app-server closed before responding")
}

// rpcError carries the app-server's own wording. Codex answers with a generic
// JSON-RPC internal error for causes as different as an expired ChatGPT session
// and a backend outage, so the message is the only distinguishing signal.
type rpcError struct {
	Code    int
	Message string
}

func (e *rpcError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("Codex RPC error %d", e.Code)
	}
	return fmt.Sprintf("Codex RPC error %d: %s", e.Code, truncate(e.Message, 400))
}

// rpcFailure keeps the upstream wording whatever it says, and additionally
// upgrades the error code when that wording names an expired session, so the UI
// can name the fix. The propagated message is the guarantee; the match below is
// a convenience, and an unrecognised wording still reaches the user intact.
func rpcFailure(home string, stderrTail func() string, err error) error {
	var rpc *rpcError
	if errors.As(err, &rpc) {
		if mentionsExpiredAuth(rpc.Message) {
			return expiredSession(home, truncate(rpc.Message, 400))
		}
		return &domain.CodedError{Code: "codex_rpc_failed", Err: err}
	}
	// A startup crash or a dead pipe says nothing on stdout; the cause is on
	// stderr. Reaping the child first is what makes that read a fact.
	tail := stderrTail()
	if tail == "" {
		return &domain.CodedError{Code: "codex_rpc_failed", Err: err}
	}
	if mentionsExpiredAuth(tail) {
		return expiredSession(home, tail)
	}
	return &domain.CodedError{Code: "codex_rpc_failed", Err: fmt.Errorf("%w: %s", err, tail)}
}

// The remediation leads the message: the poller truncates it at 240 bytes.
func expiredSession(home, cause string) error {
	return &domain.CodedError{Code: "codex_auth_required", Err: fmt.Errorf(
		"Codex session expired for CODEX_HOME %s, run `codex login`: %s", home, cause)}
}

func mentionsExpiredAuth(message string) bool {
	lowered := strings.ToLower(message)
	for _, marker := range []string{"401", "unauthorized", "token_expired", "invalid_refresh_token", "please try signing in again", "log out and sign in again"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

func truncate(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…"
}

// syncBuffer collects the child's stderr, which exec writes from a goroutine of
// its own while the RPC loop reads it.
type syncBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

const stderrCapacity = 8 << 10

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	if remaining := stderrCapacity - b.buffer.Len(); remaining > 0 {
		if len(data) > remaining {
			b.buffer.Write(data[:remaining])
		} else {
			b.buffer.Write(data)
		}
	}
	return len(data), nil
}

// lastLine returns the most recent non-empty stderr line, stripped of the ANSI
// styling the Codex tracing layer emits.
func (b *syncBuffer) lastLine() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	lines := strings.Split(b.buffer.String(), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := truncate(stripANSI(lines[index]), 200); line != "" {
			return line
		}
	}
	return ""
}

func stripANSI(value string) string {
	var builder strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] == 0x1b {
			for index++; index < len(value) && !isANSITerminator(value[index]); index++ {
			}
			continue
		}
		builder.WriteByte(value[index])
	}
	return builder.String()
}

func isANSITerminator(character byte) bool {
	return (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z')
}

type getRateLimitsResponse struct {
	RateLimits          rateLimitSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]rateLimitSnapshot `json:"rateLimitsByLimitId"`
}

type rateLimitSnapshot struct {
	LimitID   *string          `json:"limitId"`
	LimitName *string          `json:"limitName"`
	PlanType  string           `json:"planType"`
	Primary   *rateLimitWindow `json:"primary"`
	Secondary *rateLimitWindow `json:"secondary"`
	Credits   *struct {
		Balance *string `json:"balance"`
	} `json:"credits"`
}

type rateLimitWindow struct {
	UsedPercent       float64 `json:"usedPercent"`
	ResetsAt          *int64  `json:"resetsAt"`
	WindowDurationMin *int64  `json:"windowDurationMins"`
}

func rateLimitWindows(limitID string, snapshot rateLimitSnapshot) []domain.QuotaWindow {
	if snapshot.LimitID != nil && *snapshot.LimitID != "" {
		limitID = *snapshot.LimitID
	}
	label := limitID
	if snapshot.LimitName != nil && *snapshot.LimitName != "" {
		label = *snapshot.LimitName
	}
	var result []domain.QuotaWindow
	if snapshot.Primary != nil {
		result = append(result, makeWindow(limitID+":primary", label+" · primary", *snapshot.Primary))
	}
	if snapshot.Secondary != nil {
		result = append(result, makeWindow(limitID+":secondary", label+" · secondary", *snapshot.Secondary))
	}
	return result
}

func makeWindow(id, label string, raw rateLimitWindow) domain.QuotaWindow {
	window := domain.QuotaWindow{ID: id, Label: label, Kind: "rate-limit", UsedPercent: &raw.UsedPercent}
	if raw.ResetsAt != nil {
		value := time.Unix(*raw.ResetsAt, 0).UTC()
		window.ResetsAt = &value
	}
	if raw.WindowDurationMin != nil {
		window.Scope = durationLabel(*raw.WindowDurationMin)
	}
	return window
}

func durationLabel(minutes int64) string {
	if minutes%(7*24*60) == 0 {
		return fmt.Sprintf("%dw", minutes/(7*24*60))
	}
	if minutes%(24*60) == 0 {
		return fmt.Sprintf("%dd", minutes/(24*60))
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%dh", minutes/60)
	}
	return fmt.Sprintf("%dmin", minutes)
}

func cloneMeta(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func envWith(environment []string, key, value string) []string {
	prefix := key + "="
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return append(result, prefix+value)
}
