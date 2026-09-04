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
	"strconv"
	"strings"
	"time"

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
		for limitID, snapshot := range limits.RateLimitsByLimitID {
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
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, nil, &domain.CodedError{Code: "codex_start_failed", Err: errors.New("open Codex stdin")}
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, &domain.CodedError{Code: "codex_start_failed", Err: errors.New("open Codex stdout")}
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, nil, &domain.CodedError{Code: "codex_start_failed", Err: errors.New("start Codex app-server")}
	}
	defer func() {
		cancel()
		_ = command.Wait()
	}()
	encoder := json.NewEncoder(stdin)
	if err := encoder.Encode(map[string]any{
		"id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{"name": "quotadeck", "title": "QuotaDeck", "version": "0.1.0"}},
	}); err != nil {
		return nil, nil, &domain.CodedError{Code: "codex_rpc_failed", Err: errors.New("write Codex initialize request")}
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	if _, err := responseByID(scanner, 1); err != nil {
		return nil, nil, &domain.CodedError{Code: "codex_rpc_failed", Err: err}
	}
	for _, message := range []map[string]any{
		{"method": "initialized"},
		{"id": 2, "method": "account/read", "params": map[string]bool{"refreshToken": false}},
		{"id": 3, "method": "account/rateLimits/read", "params": nil},
	} {
		if err := encoder.Encode(message); err != nil {
			return nil, nil, &domain.CodedError{Code: "codex_rpc_failed", Err: errors.New("write Codex request")}
		}
	}
	accountResult, limitsResult := []byte(nil), []byte(nil)
	for accountResult == nil || limitsResult == nil {
		response, err := nextResponse(scanner)
		if err != nil {
			return nil, nil, &domain.CodedError{Code: "codex_rpc_failed", Err: err}
		}
		switch response.ID {
		case 2:
			accountResult = response.Result
		case 3:
			limitsResult = response.Result
		}
	}
	_ = stdin.Close()
	return accountResult, limitsResult, nil
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
			return response, fmt.Errorf("Codex RPC error %d", response.Error.Code)
		}
		return response, nil
	}
	if err := scanner.Err(); err != nil {
		return rpcResponse{}, errors.New("read Codex app-server response")
	}
	return rpcResponse{}, errors.New("Codex app-server closed before responding")
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
