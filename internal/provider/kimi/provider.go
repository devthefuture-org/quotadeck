package kimi

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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/domain"
)

const (
	maxResponseBytes = 4 << 20
	defaultBaseURL   = "https://api.kimi.com/coding/v1"
)

type Provider struct {
	config config.KimiConfig
	client *http.Client

	mu      sync.RWMutex
	secrets map[string]secret
}

// secret locates a credential instead of pinning it: Kimi CLI OAuth tokens
// expire after minutes and the CLI rewrites the file, so the token is
// re-resolved from the source on every fetch. Token holds the value found at
// discovery time and is used only for deduplication.
type secret struct {
	Token   string
	BaseURL string
	Kind    string // "env", "settings", or "cli"
	EnvKey  string
	Path    string
}

func (s secret) resolve() (string, error) {
	switch s.Kind {
	case "env":
		if token := strings.TrimSpace(os.Getenv(s.EnvKey)); token != "" {
			return token, nil
		}
	case "settings":
		settings, err := readClaudeSettings(s.Path)
		if err == nil && settings.Token != "" {
			return settings.Token, nil
		}
	case "cli":
		token, err := readCredentialToken(s.Path)
		if err == nil && token != "" {
			return token, nil
		}
	}
	return "", errors.New("Kimi credential source is no longer available")
}

func New(providerConfig config.KimiConfig) *Provider {
	return &Provider{
		config:  providerConfig,
		client:  &http.Client{Timeout: providerConfig.Timeout()},
		secrets: make(map[string]secret),
	}
}

func (p *Provider) ID() string   { return "kimi" }
func (p *Provider) Name() string { return "Kimi" }

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
			label = fmt.Sprintf("Kimi account %d", index+1)
		}
		ref := "env:" + keyRef
		items = append(items, discovered{
			candidate: candidate(ref, label, "environment", map[string]string{
				"keyEnv": keyRef, "secretPresent": "true",
			}),
			secret: secret{Token: token, BaseURL: baseURLFor(p.config, account.BaseURL), Kind: "env", EnvKey: keyRef},
		},
		)
	}
	// UI-managed keys remain valid even when advanced explicit accounts are
	// configured. Explicit entries are appended first and win deduplication,
	// preserving their labels and endpoint overrides.
	for _, keyRef := range []string{"KIMI_API_KEY", "KIMI_CODING_API_KEY"} {
		token := strings.TrimSpace(os.Getenv(keyRef))
		if token == "" {
			continue
		}
		ref := "env:" + keyRef
		items = append(items, discovered{
			candidate: candidate(ref, "Kimi", "environment", map[string]string{
				"keyEnv": keyRef, "secretPresent": "true",
			}),
			secret: secret{Token: token, BaseURL: baseURLFor(p.config, ""), Kind: "env", EnvKey: keyRef},
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
		ref := "file:" + filepath.Clean(path)
		items = append(items, discovered{
			candidate: candidate(ref, "Claude settings (Kimi)", "claude-settings", map[string]string{
				"path": path, "baseURL": settings.BaseURL, "secretPresent": "true",
			}),
			secret: secret{Token: settings.Token, BaseURL: codingBaseURL(settings.BaseURL), Kind: "settings", Path: path},
		})
	}
	homePaths := append([]string(nil), p.config.HomePaths...)
	if len(homePaths) == 0 {
		homePaths = []string{"~/.kimi-code"}
	}
	for _, rawHome := range homePaths {
		home := config.ExpandPath(rawHome)
		credentialFiles, err := filepath.Glob(filepath.Join(home, "credentials", "*.json"))
		if err != nil || len(credentialFiles) == 0 {
			continue
		}
		for _, credentialPath := range credentialFiles {
			token, err := readCredentialToken(credentialPath)
			if err != nil || token == "" {
				continue
			}
			ref := "file:" + filepath.Clean(credentialPath)
			items = append(items, discovered{
				candidate: candidate(ref, "Kimi Code CLI", "kimi-cli", map[string]string{
					"path": home, "secretPresent": "true",
				}),
				secret: secret{Token: token, BaseURL: baseURLFor(p.config, ""), Kind: "cli", Path: credentialPath},
			})
		}
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
	if !ok {
		return domain.Account{}, domain.Snapshot{}, &domain.CodedError{Code: "credential_missing", Err: errors.New("Kimi credential is not available")}
	}
	token, err := credentials.resolve()
	if err != nil || token == "" {
		return domain.Account{}, domain.Snapshot{}, &domain.CodedError{Code: "credential_missing", Err: err}
	}
	body, statusCode, err := p.request(ctx, credentials.BaseURL, token)
	if err != nil {
		code := "kimi_unavailable"
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden {
			code = "kimi_auth_error"
			if statusCode == http.StatusUnauthorized && credentials.Kind == "cli" {
				err = errors.New("Kimi CLI credential expired; use Kimi Code once to refresh it, or configure an explicit Kimi account in the config")
			}
		}
		return domain.Account{}, domain.Snapshot{}, &domain.CodedError{Code: code, Err: err}
	}
	windows, err := Parse(body)
	if err != nil {
		return domain.Account{}, domain.Snapshot{}, err
	}
	resultAccount := domain.Account{
		ID: account.ID, ProviderID: "kimi", Label: account.Label,
		Plan: "Kimi for Coding", Source: account.Source, SourceMeta: cloneMeta(account.SourceMeta),
	}
	snapshot, err := domain.NormalizeSnapshot(domain.Snapshot{
		AccountID: account.ID, FetchedAt: time.Now().UTC(), Status: domain.StatusFresh, Windows: windows,
	})
	if err != nil {
		return resultAccount, domain.Snapshot{}, &domain.CodedError{Code: "invalid_usage", Err: err}
	}
	return resultAccount, snapshot, nil
}

// Parse turns a /usages payload into quota windows. The endpoint has shipped
// several payload shapes; all known ones are accepted and unknown fields are
// ignored.
func Parse(body []byte) ([]domain.QuotaWindow, error) {
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, &domain.CodedError{Code: "invalid_json", Err: errors.New("Kimi returned invalid JSON")}
	}
	// The ratio map is the primary plan view: limit_5h, limit_month_total,
	// limit_month_code, and any future limit_* key.
	windows := make([]domain.QuotaWindow, 0, 4)
	seen := make(map[string]int)
	appendWindow := func(window domain.QuotaWindow) error {
		normalized, err := domain.NormalizeWindow(window)
		if err != nil {
			return err
		}
		seen[normalized.ID]++
		if seen[normalized.ID] > 1 {
			normalized.ID = fmt.Sprintf("%s-%d", normalized.ID, seen[normalized.ID])
		}
		windows = append(windows, normalized)
		return nil
	}
	if usages, ok := root["usages"].(map[string]any); ok {
		for _, key := range sortedKeys(usages) {
			entry, ok := usages[key].(map[string]any)
			if !ok {
				continue
			}
			if err := appendWindow(ratioWindow(key, entry)); err != nil {
				return nil, &domain.CodedError{Code: "invalid_limit", Err: err}
			}
		}
	}
	rawLimits, _ := root["limits"].([]any)
	for index, raw := range rawLimits {
		limit, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if err := appendWindow(limitWindow(limit, index)); err != nil {
			return nil, &domain.CodedError{Code: "invalid_limit", Err: err}
		}
	}
	if usage, ok := root["usage"].(map[string]any); ok {
		if err := appendWindow(ratioWindow("usage", usage)); err != nil {
			return nil, &domain.CodedError{Code: "invalid_limit", Err: err}
		}
	}
	rawData, _ := root["data"].([]any)
	for index, raw := range rawData {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if err := appendWindow(dataWindow(entry, index)); err != nil {
			return nil, &domain.CodedError{Code: "invalid_limit", Err: err}
		}
	}
	return windows, nil
}

// ratioWindow builds a window from a {used_ratio, reset_time} style entry,
// falling back to used/limit amounts when ratios are absent.
func ratioWindow(key string, entry map[string]any) domain.QuotaWindow {
	window := domain.QuotaWindow{
		ID:    "usages-" + slug(key),
		Label: usageLabel(key),
		Kind:  "quota",
	}
	if ratio, ok := firstNumber(entry, "used_ratio", "usedRatio"); ok {
		percent := ratio * 100
		window.UsedPercent = &percent
		remaining := 100 - percent
		window.RemainingPercent = &remaining
	} else if used, ok := firstNumber(entry, "used", "used_amount"); ok {
		if total, ok := firstNumber(entry, "limit", "limit_amount"); ok && total > 0 {
			window.Used = &used
			window.Limit = &total
		}
	}
	window.ResetsAt = parseReset(entry)
	return window
}

// limitWindow builds a window from a limits[] entry carrying a billing window
// and concrete amounts: {window: {duration, timeUnit}, detail: {...}}.
func limitWindow(limit map[string]any, index int) domain.QuotaWindow {
	detail, _ := limit["detail"].(map[string]any)
	if detail == nil {
		detail = limit
	}
	billingWindow, _ := limit["window"].(map[string]any)
	minutes := windowMinutes(billingWindow)
	label := firstString(detail, "name", "title")
	if label == "" {
		label = "Limit"
		if minutes > 0 {
			label = humanDuration(minutes) + " limit"
		}
	}
	idSeed := firstString(detail, "id")
	if idSeed == "" {
		idSeed = fmt.Sprintf("limit-%s", humanDurationOrIndex(minutes, index))
	}
	window := domain.QuotaWindow{ID: slug(idSeed), Label: label, Kind: "quota"}
	if used, ok := firstNumber(detail, "used", "used_amount"); ok {
		window.Used = &used
	}
	if total, ok := firstNumber(detail, "limit", "limit_amount"); ok {
		window.Limit = &total
	}
	if remaining, ok := firstNumber(detail, "remaining", "remaining_amount"); ok {
		window.Remaining = &remaining
	}
	window.ResetsAt = parseReset(detail)
	return window
}

// dataWindow builds a window from a data[] entry keyed by model name.
func dataWindow(entry map[string]any, index int) domain.QuotaWindow {
	modelName := firstString(entry, "model_name", "name", "model")
	label := "Limit"
	if modelName == "all" {
		label = "All models"
	} else if modelName != "" {
		label = modelName
	}
	window := domain.QuotaWindow{
		ID:    "data-" + slug(firstNonEmpty(modelName, fmt.Sprintf("entry-%d", index+1))),
		Label: label,
		Kind:  "quota",
		Scope: modelName,
	}
	if used, ok := firstNumber(entry, "used", "used_amount"); ok {
		window.Used = &used
	}
	if total, ok := firstNumber(entry, "limit", "limit_amount"); ok {
		window.Limit = &total
	}
	if remaining, ok := firstNumber(entry, "remaining", "remaining_amount"); ok {
		window.Remaining = &remaining
	}
	if ratio, ok := firstNumber(entry, "used_ratio", "usedRatio", "percentage"); ok {
		window.UsedPercent = &ratio
		remaining := 100 - ratio
		window.RemainingPercent = &remaining
	}
	window.ResetsAt = parseReset(entry)
	return window
}

func windowMinutes(billingWindow map[string]any) float64 {
	if billingWindow == nil {
		return 0
	}
	duration, hasDuration := firstNumber(billingWindow, "duration")
	unit := firstString(billingWindow, "timeUnit", "time_unit")
	if !hasDuration || duration <= 0 {
		return 0
	}
	switch strings.ToUpper(unit) {
	case "TIME_UNIT_MINUTE":
		return duration
	case "TIME_UNIT_HOUR":
		return duration * 60
	case "TIME_UNIT_DAY":
		return duration * 24 * 60
	case "TIME_UNIT_MONTH":
		return duration * 30 * 24 * 60
	default:
		return 0
	}
}

func parseReset(entry map[string]any) *time.Time {
	if value, ok := firstValue(entry, "resetTime", "reset_time", "reset_at", "reset_time_at"); ok {
		if parsed := parseAbsoluteTime(value); parsed != nil {
			return parsed
		}
	}
	if seconds, ok := firstNumber(entry, "reset_in", "resetIn"); ok && seconds > 0 {
		reset := time.Now().UTC().Add(time.Duration(seconds) * time.Second)
		return &reset
	}
	return nil
}

func parseAbsoluteTime(value any) *time.Time {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if unix, err := strconv.ParseInt(text, 10, 64); err == nil {
			return unixTime(unix)
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, text); err == nil {
				parsed = parsed.UTC()
				return &parsed
			}
		}
	case float64:
		return unixTime(int64(typed))
	case json.Number:
		if unix, err := typed.Int64(); err == nil {
			return unixTime(unix)
		}
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

// recognizedBaseURL accepts only Kimi endpoints, so an unrelated
// ANTHROPIC_AUTH_TOKEN sitting next to another proxy is never promoted.
func recognizedBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.kimi.com" || strings.HasSuffix(host, ".kimi.com")
}

// codingBaseURL derives the coding API base from an Anthropic-compatible Kimi
// endpoint, keeping explicit scheme and host while dropping the path.
func codingBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return defaultBaseURL
	}
	return parsed.Scheme + "://" + parsed.Host + "/coding/v1"
}

func baseURLFor(providerConfig config.KimiConfig, accountBaseURL string) string {
	raw := strings.TrimSpace(accountBaseURL)
	if raw == "" {
		raw = strings.TrimSpace(providerConfig.BaseURL)
	}
	if raw == "" {
		return defaultBaseURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return defaultBaseURL
	}
	return raw
}

func readCredentialToken(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var root struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return "", err
	}
	return strings.TrimSpace(root.AccessToken), nil
}

func candidate(ref, label, source string, meta map[string]string) domain.AccountCandidate {
	sum := sha256.Sum256([]byte(ref))
	return domain.AccountCandidate{
		ID: "kimi:" + slug(label) + ":" + hex.EncodeToString(sum[:4]), ProviderID: "kimi",
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

func (p *Provider) request(ctx context.Context, baseURL, token string) ([]byte, int, error) {
	maxRetries := p.config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	if maxRetries > 5 {
		maxRetries = 5
	}
	endpoints := []string{strings.TrimRight(baseURL, "/") + "/usages", strings.TrimRight(baseURL, "/") + "/usage"}
	var lastStatus int
	for attempt := 0; attempt <= maxRetries; attempt++ {
		body, statusCode, err := p.attempt(ctx, endpoints[0], token)
		if err != nil && statusCode == http.StatusNotFound {
			body, statusCode, err = p.attempt(ctx, endpoints[1], token)
		}
		if err == nil {
			return body, statusCode, nil
		}
		lastStatus = statusCode
		if statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || statusCode == http.StatusNotFound {
			return nil, statusCode, err
		}
		if attempt == maxRetries {
			break
		}
		if (statusCode == http.StatusTooManyRequests || statusCode >= 500 || statusCode == 0) && attempt < maxRetries {
			if err := sleep(ctx, backoff(attempt, "")); err != nil {
				return nil, statusCode, err
			}
			continue
		}
		break
	}
	return nil, lastStatus, fmt.Errorf("Kimi returned HTTP %d", lastStatus)
}

func (p *Provider) attempt(ctx context.Context, endpoint, token string) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := p.client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	_ = response.Body.Close()
	if readErr != nil {
		return nil, response.StatusCode, errors.New("read Kimi response")
	}
	if len(body) > maxResponseBytes {
		return nil, response.StatusCode, errors.New("Kimi response is too large")
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return body, response.StatusCode, nil
	}
	return nil, response.StatusCode, fmt.Errorf("Kimi returned HTTP %d", response.StatusCode)
}

func usageLabel(key string) string {
	switch strings.ToLower(key) {
	case "limit_5h", "5h":
		return "5h window"
	case "limit_month_total", "month_total", "total":
		return "Monthly total"
	case "limit_month_code", "month_code", "code":
		return "Monthly coding"
	}
	return humanType(key)
}

func humanDuration(minutes float64) string {
	if minutes >= 7*24*60 && math.Mod(minutes, 7*24*60) == 0 {
		return fmt.Sprintf("%gw", minutes/(7*24*60))
	}
	if minutes >= 30*24*60 && math.Mod(minutes, 30*24*60) == 0 {
		return fmt.Sprintf("%gmo", minutes/(30*24*60))
	}
	if minutes >= 24*60 && math.Mod(minutes, 24*60) == 0 {
		return fmt.Sprintf("%gd", minutes/(24*60))
	}
	if minutes >= 60 && math.Mod(minutes, 60) == 0 {
		return fmt.Sprintf("%gh", minutes/60)
	}
	return fmt.Sprintf("%gmin", minutes)
}

func humanDurationOrIndex(minutes float64, index int) string {
	if minutes > 0 {
		return humanDuration(minutes)
	}
	return strconv.Itoa(index + 1)
}

func humanType(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ToLower(value), "_", " "))
	if value == "" {
		return "Quota"
	}
	return strings.ToUpper(value[:1]) + value[1:]
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
		if err != nil {
			return 0, false
		}
		return result, !math.IsNaN(result) && !math.IsInf(result, 0)
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

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
