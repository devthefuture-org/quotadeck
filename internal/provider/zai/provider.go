package zai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/domain"
)

const maxResponseBytes = 4 << 20

type Provider struct {
	config config.ZAIConfig
	client *http.Client

	mu      sync.RWMutex
	secrets map[string]secret
}

type secret struct {
	Token        string
	Region       string
	Organization string
	Workspace    string
	BaseURL      string
}

func New(providerConfig config.ZAIConfig) *Provider {
	return &Provider{
		config:  providerConfig,
		client:  &http.Client{Timeout: providerConfig.Timeout()},
		secrets: make(map[string]secret),
	}
}

func (p *Provider) ID() string   { return "zai" }
func (p *Provider) Name() string { return "Z.ai / GLM" }

func (p *Provider) Discover(_ context.Context) ([]domain.AccountCandidate, error) {
	type discovered struct {
		candidate domain.AccountCandidate
		secret    secret
	}
	var items []discovered
	for index, account := range p.config.Accounts {
		keyRef := strings.TrimSpace(account.KeyEnv)
		if keyRef == "" {
			continue
		}
		token := strings.TrimSpace(os.Getenv(keyRef))
		if token == "" {
			continue
		}
		label := strings.TrimSpace(account.Label)
		if label == "" {
			label = fmt.Sprintf("Z.ai account %d", index+1)
		}
		region := normalizeRegion(account.Region)
		ref := "env:" + keyRef
		items = append(items, discovered{
			candidate: candidate(ref, label, "environment", map[string]string{
				"keyEnv": keyRef, "region": region, "secretPresent": "true",
			}),
			secret: secret{
				Token: token, Region: region,
				Organization: strings.TrimSpace(os.Getenv(account.OrganizationIDEnv)),
				Workspace:    strings.TrimSpace(os.Getenv(account.WorkspaceIDEnv)),
			},
		})
	}
	// UI-managed keys remain valid even when advanced explicit accounts are
	// configured. Explicit entries are appended first and win deduplication,
	// preserving their labels and optional organization/workspace metadata.
	for _, keyRef := range []string{"ZAI_API_KEY", "GLM_API_KEY"} {
		token := strings.TrimSpace(os.Getenv(keyRef))
		if token == "" {
			continue
		}
		region := "global"
		if keyRef == "GLM_API_KEY" {
			region = "china"
		}
		ref := "env:" + keyRef
		items = append(items, discovered{
			candidate: candidate(ref, "Z.ai", "environment", map[string]string{
				"keyEnv": keyRef, "region": region, "secretPresent": "true",
			}),
			secret: secret{Token: token, Region: region},
		})
	}
	settingsPaths := append([]string(nil), p.config.SettingsPaths...)
	if len(settingsPaths) == 0 {
		settingsPaths = []string{"~/.claude/settings.json"}
	}
	for _, rawPath := range settingsPaths {
		path := config.ExpandPath(rawPath)
		settings, err := readClaudeSettings(path)
		if err != nil {
			continue
		}
		if settings.Token == "" || !recognizedBaseURL(settings.BaseURL) {
			continue
		}
		region := "global"
		if strings.Contains(strings.ToLower(settings.BaseURL), "bigmodel.cn") {
			region = "china"
		}
		ref := "file:" + filepath.Clean(path)
		items = append(items, discovered{
			candidate: candidate(ref, "Claude settings (Z.ai)", "claude-settings", map[string]string{
				"path": path, "baseURL": settings.BaseURL, "region": region, "secretPresent": "true",
			}),
			secret: secret{Token: settings.Token, Region: region, BaseURL: settings.BaseURL},
		})
	}

	seenTokens := make(map[string]struct{})
	secrets := make(map[string]secret)
	result := make([]domain.AccountCandidate, 0, len(items))
	for _, item := range items {
		fingerprint := tokenFingerprint(item.secret.Token)
		if _, exists := seenTokens[fingerprint]; exists {
			continue
		}
		seenTokens[fingerprint] = struct{}{}
		secrets[item.candidate.Ref] = item.secret
		result = append(result, item.candidate)
	}
	p.mu.Lock()
	p.secrets = secrets
	p.mu.Unlock()
	return result, nil
}

func (p *Provider) Fetch(ctx context.Context, account domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	p.mu.RLock()
	credentials, ok := p.secrets[account.Ref]
	p.mu.RUnlock()
	if !ok {
		if _, err := p.Discover(ctx); err != nil {
			return domain.Account{}, domain.Snapshot{}, err
		}
		p.mu.RLock()
		credentials, ok = p.secrets[account.Ref]
		p.mu.RUnlock()
	}
	if !ok || credentials.Token == "" {
		return domain.Account{}, domain.Snapshot{}, &domain.CodedError{Code: "credential_missing", Err: errors.New("Z.ai credential is not available")}
	}
	endpoint, err := p.endpoint(credentials)
	if err != nil {
		return domain.Account{}, domain.Snapshot{}, err
	}
	body, statusCode, err := p.request(ctx, endpoint, credentials)
	if err != nil {
		code := "zai_unavailable"
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			code = "zai_auth_error"
		}
		return domain.Account{}, domain.Snapshot{}, &domain.CodedError{Code: code, Err: err}
	}
	plan, windows, err := Parse(body)
	if err != nil {
		return domain.Account{}, domain.Snapshot{}, err
	}
	resultAccount := domain.Account{
		ID: account.ID, ProviderID: "zai", Label: account.Label, Plan: plan,
		Source: account.Source, SourceMeta: cloneMeta(account.SourceMeta),
	}
	snapshot, err := domain.NormalizeSnapshot(domain.Snapshot{
		AccountID: account.ID, FetchedAt: time.Now().UTC(), Status: domain.StatusFresh, Windows: windows,
	})
	if err != nil {
		return resultAccount, domain.Snapshot{}, &domain.CodedError{Code: "invalid_usage", Err: err}
	}
	return resultAccount, snapshot, nil
}

func Parse(body []byte) (string, []domain.QuotaWindow, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return "", nil, &domain.CodedError{Code: "invalid_json", Err: errors.New("Z.ai returned invalid JSON")}
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		data = root
	}
	plan := firstString(data, "planName", "plan", "plan_type", "packageName", "level")
	rawLimits, _ := data["limits"].([]any)
	windows := make([]domain.QuotaWindow, 0, len(rawLimits))
	seen := make(map[string]int)
	for index, raw := range rawLimits {
		limit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		window := parseLimit(limit, index)
		baseID := window.ID
		seen[baseID]++
		if seen[baseID] > 1 {
			window.ID = fmt.Sprintf("%s-%d", baseID, seen[baseID])
		}
		normalized, err := domain.NormalizeWindow(window)
		if err != nil {
			return "", nil, &domain.CodedError{Code: "invalid_limit", Err: err}
		}
		windows = append(windows, normalized)
	}
	return plan, windows, nil
}

func (p *Provider) endpoint(credentials secret) (string, error) {
	raw := strings.TrimSpace(p.config.QuotaURL)
	if raw == "" {
		if credentials.Region == "china" {
			raw = "https://open.bigmodel.cn/api/monitor/usage/quota/limit"
		} else {
			raw = "https://api.z.ai/api/monitor/usage/quota/limit"
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return "", &domain.CodedError{Code: "unsafe_endpoint", Err: errors.New("Z.ai quota URL must be an absolute HTTPS URL")}
	}
	if credentials.Organization != "" || credentials.Workspace != "" {
		query := parsed.Query()
		query.Set("type", "2")
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), nil
}

func (p *Provider) request(ctx context.Context, endpoint string, credentials secret) ([]byte, int, error) {
	maxRetries := p.config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 5 {
		maxRetries = 5
	}
	for attempt := 0; attempt <= maxRetries; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, 0, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+credentials.Token)
		if credentials.Organization != "" {
			request.Header.Set("Bigmodel-Organization", credentials.Organization)
		}
		if credentials.Workspace != "" {
			request.Header.Set("Bigmodel-Project", credentials.Workspace)
		}
		response, err := p.client.Do(request)
		if err != nil {
			if attempt == maxRetries {
				return nil, 0, errors.New("Z.ai request failed")
			}
			if err := sleep(ctx, backoff(attempt, "")); err != nil {
				return nil, 0, err
			}
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		_ = response.Body.Close()
		if readErr != nil {
			return nil, response.StatusCode, errors.New("read Z.ai response")
		}
		if len(body) > maxResponseBytes {
			return nil, response.StatusCode, errors.New("Z.ai response is too large")
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			return body, response.StatusCode, nil
		}
		if (response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500) && attempt < maxRetries {
			if err := sleep(ctx, backoff(attempt, response.Header.Get("Retry-After"))); err != nil {
				return nil, response.StatusCode, err
			}
			continue
		}
		return nil, response.StatusCode, fmt.Errorf("Z.ai returned HTTP %d", response.StatusCode)
	}
	return nil, 0, errors.New("Z.ai request failed")
}

func parseLimit(limit map[string]any, index int) domain.QuotaWindow {
	typeName := firstString(limit, "type", "kind", "name")
	unit := firstString(limit, "unit")
	durationValue, _ := firstNumber(limit, "number", "windowMinutes", "window_minutes")
	windowMinutes, hasMinutes := firstNumber(limit, "windowMinutes", "window_minutes")
	if !hasMinutes && durationValue > 0 {
		windowMinutes = durationToMinutes(durationValue, unit)
	}
	label := firstString(limit, "label", "displayName", "name")
	if label == "" {
		label = humanType(typeName)
		if windowMinutes > 0 {
			label += " · " + humanDuration(windowMinutes)
		}
	}
	idSeed := firstString(limit, "id", "limitId")
	if idSeed == "" {
		idSeed = typeName + "-" + strconv.FormatFloat(windowMinutes, 'f', -1, 64)
	}
	if strings.Trim(idSeed, "-") == "" {
		idSeed = fmt.Sprintf("limit-%d", index+1)
	}
	window := domain.QuotaWindow{
		ID: slug(idSeed), Label: label, Kind: kind(typeName), Scope: firstString(limit, "scope", "model"), Unit: unit,
	}
	if used, ok := firstNumber(limit, "usage", "used", "currentValue", "current_value"); ok {
		window.Used = &used
	}
	if total, ok := firstNumber(limit, "limit", "total", "max", "quota"); ok {
		window.Limit = &total
	}
	if remaining, ok := firstNumber(limit, "remaining", "remainingValue", "remaining_value"); ok {
		window.Remaining = &remaining
	}
	if percentage, ok := firstNumber(limit, "percentage", "usedPercent", "used_percentage"); ok {
		window.UsedPercent = &percentage
	} else if window.Used != nil && window.Limit != nil && *window.Limit > 0 {
		percentage := *window.Used / *window.Limit * 100
		window.UsedPercent = &percentage
	} else if window.Remaining != nil && window.Limit != nil && *window.Limit > 0 {
		percentage := 100 - (*window.Remaining / *window.Limit * 100)
		window.UsedPercent = &percentage
	}
	window.ResetsAt = parseReset(limit)
	return window
}

func parseReset(limit map[string]any) *time.Time {
	value, ok := firstValue(limit, "nextResetTime", "resetTime", "resetsAt", "reset_at")
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case string:
		if unix, err := strconv.ParseInt(typed, 10, 64); err == nil {
			return unixTime(unix)
		}
		if parsed, err := time.Parse(time.RFC3339, typed); err == nil {
			parsed = parsed.UTC()
			return &parsed
		}
	case float64:
		return unixTime(int64(typed))
	}
	return nil
}

func unixTime(raw int64) *time.Time {
	if raw > 10_000_000_000 {
		raw /= 1000
	}
	if raw <= 0 {
		return nil
	}
	value := time.Unix(raw, 0).UTC()
	return &value
}

type claudeSettings struct {
	Token   string
	BaseURL string
}

func readClaudeSettings(path string) (claudeSettings, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return claudeSettings{}, err
	}
	var root struct {
		Env map[string]any `json:"env"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return claudeSettings{}, err
	}
	return claudeSettings{
		Token:   stringValue(root.Env["ANTHROPIC_AUTH_TOKEN"]),
		BaseURL: stringValue(root.Env["ANTHROPIC_BASE_URL"]),
	}, nil
}

func recognizedBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.z.ai" || host == "z.ai" || strings.HasSuffix(host, ".z.ai") || host == "open.bigmodel.cn" || strings.HasSuffix(host, ".bigmodel.cn")
}

func candidate(ref, label, source string, meta map[string]string) domain.AccountCandidate {
	sum := sha256.Sum256([]byte(ref))
	return domain.AccountCandidate{
		ID: "zai:" + slug(label) + ":" + hex.EncodeToString(sum[:4]), ProviderID: "zai",
		Label: label, Source: source, SourceMeta: meta, Ref: ref,
	}
}

func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func cloneMeta(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func normalizeRegion(region string) string {
	switch strings.ToLower(strings.TrimSpace(region)) {
	case "cn", "china", "bigmodel", "bigmodel-cn":
		return "china"
	default:
		return "global"
	}
}

func firstString(values map[string]any, keys ...string) string {
	value, ok := firstValue(values, keys...)
	if !ok {
		return ""
	}
	return stringValue(value)
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}

func firstNumber(values map[string]any, keys ...string) (float64, bool) {
	value, ok := firstValue(values, keys...)
	if !ok {
		return 0, false
	}
	switch typed := value.(type) {
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	case string:
		result, err := strconv.ParseFloat(typed, 64)
		return result, err == nil
	default:
		return 0, false
	}
}

func firstValue(values map[string]any, keys ...string) (any, bool) {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			return value, true
		}
	}
	return nil, false
}

func durationToMinutes(value float64, unit string) float64 {
	switch strings.ToLower(unit) {
	case "minute", "minutes", "min":
		return value
	case "hour", "hours", "h":
		return value * 60
	case "day", "days", "d":
		return value * 24 * 60
	case "week", "weeks", "w":
		return value * 7 * 24 * 60
	default:
		return 0
	}
}

func humanDuration(minutes float64) string {
	if minutes >= 7*24*60 && math.Mod(minutes, 7*24*60) == 0 {
		return fmt.Sprintf("%gw", minutes/(7*24*60))
	}
	if minutes >= 24*60 && math.Mod(minutes, 24*60) == 0 {
		return fmt.Sprintf("%gd", minutes/(24*60))
	}
	if minutes >= 60 && math.Mod(minutes, 60) == 0 {
		return fmt.Sprintf("%gh", minutes/60)
	}
	return fmt.Sprintf("%gmin", minutes)
}

func humanType(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ToLower(value), "_", " "))
	if value == "" {
		return "Quota"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func kind(typeName string) string {
	upper := strings.ToUpper(typeName)
	if strings.Contains(upper, "TIME") || strings.Contains(upper, "MCP") {
		return "mcp"
	}
	if strings.Contains(upper, "TOKEN") {
		return "tokens"
	}
	return "quota"
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
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "quota"
	}
	return result
}

func backoff(attempt int, retryAfter string) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && seconds > 0 {
		if seconds > 30 {
			seconds = 30
		}
		return time.Duration(seconds) * time.Second
	}
	base := time.Second * time.Duration(1<<min(attempt, 4))
	jitter := time.Duration(rand.IntN(500)) * time.Millisecond
	return base + jitter
}

func sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
