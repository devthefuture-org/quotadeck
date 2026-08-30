package claudecswap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/runner"
)

const cacheTTL = 3 * time.Second

type Provider struct {
	binary string
	runner runner.Runner

	mu       sync.Mutex
	cachedAt time.Time
	cached   payload
}

func New(binary string, commandRunner runner.Runner) *Provider {
	if binary == "" {
		binary = "cswap"
	}
	if commandRunner == nil {
		commandRunner = runner.ExecRunner{}
	}
	return &Provider{binary: binary, runner: commandRunner}
}

func (p *Provider) ID() string   { return "claude" }
func (p *Provider) Name() string { return "Claude" }

func (p *Provider) Discover(ctx context.Context) ([]domain.AccountCandidate, error) {
	if _, err := p.runner.LookPath(p.binary); err != nil {
		return nil, &domain.CodedError{Code: "cswap_not_found", Err: errors.New("cswap executable not found")}
	}
	data, err := p.load(ctx, true)
	if err != nil {
		return nil, err
	}
	candidates := make([]domain.AccountCandidate, 0, len(data.Accounts))
	for index, item := range data.Accounts {
		slot := item.Number
		if slot <= 0 {
			slot = index + 1
		}
		label := accountLabel(item, index)
		candidates = append(candidates, domain.AccountCandidate{
			ID:         fmt.Sprintf("claude:cswap:%d", slot),
			ProviderID: "claude",
			Label:      label,
			Source:     "cswap",
			SourceMeta: map[string]string{"slot": strconv.Itoa(slot), "usageStatus": item.UsageStatus},
			Ref:        strconv.Itoa(slot),
		})
	}
	return candidates, nil
}

func (p *Provider) Fetch(ctx context.Context, candidate domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	data, err := p.load(ctx, false)
	if err != nil {
		return domain.Account{}, domain.Snapshot{}, err
	}
	slot, _ := strconv.Atoi(candidate.Ref)
	var item *rawAccount
	for index := range data.Accounts {
		if data.Accounts[index].Number == slot {
			item = &data.Accounts[index]
			break
		}
	}
	if item == nil {
		return domain.Account{}, domain.Snapshot{}, &domain.CodedError{Code: "account_missing", Err: errors.New("cswap account disappeared")}
	}
	account := domain.Account{
		ID:         candidate.ID,
		ProviderID: "claude",
		Label:      accountLabel(*item, slot-1),
		Plan:       item.Plan,
		Active:     item.Active,
		Disabled:   item.Disabled,
		Source:     "cswap",
		SourceMeta: map[string]string{"slot": strconv.Itoa(slot), "usageStatus": item.UsageStatus},
	}
	usage := item.Usage
	stale := false
	status := statusFor(item.UsageStatus)
	age := seconds(item.UsageAgeSeconds)
	if usage == nil && item.LastGoodUsage != nil {
		usage = item.LastGoodUsage
		stale = true
		status = domain.StatusStale
		age = seconds(item.LastGoodAgeSeconds)
	}
	snapshot := domain.Snapshot{
		AccountID:    account.ID,
		FetchedAt:    time.Now().UTC(),
		SourceAgeSec: age,
		Status:       status,
		Stale:        stale || status != domain.StatusFresh,
		Windows:      windows(usage),
	}
	if item.Disabled {
		snapshot.Status = domain.StatusUnsupported
		snapshot.Stale = true
		snapshot.ErrorCode = "account_disabled"
		snapshot.ErrorMessage = "account disabled in cswap"
	}
	if item.UsageStatus != "" && item.UsageStatus != "ok" && snapshot.ErrorCode == "" {
		snapshot.ErrorCode = "cswap_" + slug(item.UsageStatus)
		snapshot.ErrorMessage = "cswap reports " + item.UsageStatus
	}
	normalized, err := domain.NormalizeSnapshot(snapshot)
	if err != nil {
		return account, snapshot, &domain.CodedError{Code: "invalid_usage", Err: err}
	}
	return account, normalized, nil
}

func Parse(raw []byte) ([]domain.AccountState, error) {
	var data payload
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, &domain.CodedError{Code: "invalid_json", Err: fmt.Errorf("decode cswap output: %w", err)}
	}
	if data.SchemaVersion != 1 {
		return nil, &domain.CodedError{Code: "unsupported_schema", Err: fmt.Errorf("unsupported cswap schema version %d", data.SchemaVersion)}
	}
	if data.Error != nil {
		return nil, &domain.CodedError{Code: "cswap_error", Err: errors.New("cswap returned an error")}
	}
	result := make([]domain.AccountState, 0, len(data.Accounts))
	provider := &Provider{cached: data, cachedAt: time.Now()}
	for index, item := range data.Accounts {
		slot := item.Number
		if slot <= 0 {
			slot = index + 1
		}
		candidate := domain.AccountCandidate{
			ID: fmt.Sprintf("claude:cswap:%d", slot), ProviderID: "claude",
			Label: accountLabel(item, index), Source: "cswap", Ref: strconv.Itoa(slot),
		}
		account, snapshot, err := provider.Fetch(context.Background(), candidate)
		if err != nil {
			return nil, err
		}
		result = append(result, domain.AccountState{Account: account, Snapshot: snapshot})
	}
	return result, nil
}

func (p *Provider) load(ctx context.Context, force bool) (payload, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !force && !p.cachedAt.IsZero() && time.Since(p.cachedAt) < cacheTTL {
		return p.cached, nil
	}
	result, runErr := p.runner.Run(ctx, p.binary, "list", "--json")
	var data payload
	if err := json.Unmarshal(result.Stdout, &data); err != nil {
		if runErr != nil {
			return payload{}, &domain.CodedError{Code: "cswap_failed", Err: runErr}
		}
		return payload{}, &domain.CodedError{Code: "invalid_json", Err: errors.New("cswap returned invalid JSON")}
	}
	if data.SchemaVersion != 1 {
		return payload{}, &domain.CodedError{Code: "unsupported_schema", Err: fmt.Errorf("unsupported cswap schema version %d", data.SchemaVersion)}
	}
	if data.Error != nil || runErr != nil {
		return payload{}, &domain.CodedError{Code: "cswap_failed", Err: errors.New("cswap could not list accounts")}
	}
	p.cached, p.cachedAt = data, time.Now()
	return data, nil
}

type payload struct {
	SchemaVersion       int          `json:"schemaVersion"`
	ActiveAccountNumber int          `json:"activeAccountNumber"`
	Accounts            []rawAccount `json:"accounts"`
	Error               any          `json:"error"`
}

type rawAccount struct {
	Number             int       `json:"number"`
	Alias              string    `json:"alias"`
	Email              string    `json:"email"`
	Plan               string    `json:"plan"`
	Active             bool      `json:"active"`
	Disabled           bool      `json:"disabled"`
	UsageStatus        string    `json:"usageStatus"`
	Usage              *rawUsage `json:"usage"`
	UsageAgeSeconds    *float64  `json:"usageAgeSeconds"`
	LastGoodUsage      *rawUsage `json:"lastGoodUsage"`
	LastGoodAgeSeconds *float64  `json:"lastGoodAgeSeconds"`
}

type rawUsage struct {
	FiveHour *rawWindow  `json:"fiveHour"`
	SevenDay *rawWindow  `json:"sevenDay"`
	Scoped   []rawScoped `json:"scoped"`
}

type rawWindow struct {
	Pct                   *float64 `json:"pct"`
	ResetsAt              string   `json:"resetsAt"`
	ExpectedPct           *float64 `json:"expectedPct"`
	ProjectedExhaustionAt string   `json:"projectedExhaustionAt"`
	WillLastToReset       *bool    `json:"willLastToReset"`
}

type rawScoped struct {
	Name string `json:"name"`
	rawWindow
}

func accountLabel(item rawAccount, index int) string {
	if strings.TrimSpace(item.Alias) != "" {
		return strings.TrimSpace(item.Alias)
	}
	if strings.TrimSpace(item.Email) != "" {
		return strings.TrimSpace(item.Email)
	}
	return fmt.Sprintf("Claude account %d", index+1)
}

func statusFor(status string) string {
	switch status {
	case "", "ok":
		return domain.StatusFresh
	case "token_expired", "auth_error", "unauthorized":
		return domain.StatusAuthError
	case "unavailable", "error", "rate_limited":
		return domain.StatusUnavailable
	default:
		return domain.StatusUnsupported
	}
}

func windows(usage *rawUsage) []domain.QuotaWindow {
	if usage == nil {
		return []domain.QuotaWindow{}
	}
	result := make([]domain.QuotaWindow, 0, 2+len(usage.Scoped))
	if usage.FiveHour != nil {
		result = append(result, quotaWindow("five-hour", "5 hours", "rolling", "all", *usage.FiveHour))
	}
	if usage.SevenDay != nil {
		result = append(result, quotaWindow("seven-day", "7 days", "weekly", "all", *usage.SevenDay))
	}
	for index, item := range usage.Scoped {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = fmt.Sprintf("Scoped limit %d", index+1)
		}
		result = append(result, quotaWindow("scoped-"+slug(name), name, "weekly", name, item.rawWindow))
	}
	return result
}

func quotaWindow(id, label, kind, scope string, raw rawWindow) domain.QuotaWindow {
	window := domain.QuotaWindow{
		ID: id, Label: label, Kind: kind, Scope: scope,
		UsedPercent: raw.Pct, ExpectedPercent: raw.ExpectedPct,
		WillLastToReset: raw.WillLastToReset,
	}
	window.ResetsAt = parseTime(raw.ResetsAt)
	window.ProjectedExhaustionAt = parseTime(raw.ProjectedExhaustionAt)
	return window
}

func parseTime(raw string) *time.Time {
	if raw == "" {
		return nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	value = value.UTC()
	return &value
}

func seconds(value *float64) *int64 {
	if value == nil {
		return nil
	}
	result := int64(*value)
	return &result
}

func slug(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			builder.WriteRune(character)
			lastDash = false
		} else if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}
