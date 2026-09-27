package kimi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/domain"
)

func TestParseReadsLivePayload(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "kimi", "usages.json"))
	if err != nil {
		t.Fatal(err)
	}
	windows, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 4 {
		t.Fatalf("expected 4 windows (5h ratio, monthly total, monthly coding, 5h limit), got %d: %#v", len(windows), windows)
	}
	// Sorted keys put limit_5h first, the limits[] entry last.
	byID := make(map[string]domain.QuotaWindow, len(windows))
	for _, window := range windows {
		byID[window.ID] = window
	}
	fiveHours, ok := byID["usages-limit-5h"]
	if !ok || fiveHours.UsedPercent == nil || *fiveHours.UsedPercent != 0 {
		t.Fatalf("5h ratio window missing or wrong: %#v", fiveHours)
	}
	monthly, ok := byID["usages-limit-month-total"]
	if !ok || monthly.UsedPercent == nil || *monthly.UsedPercent != 2.01 {
		t.Fatalf("monthly total window missing or wrong: %#v", monthly)
	}
	if monthly.ResetsAt == nil || !monthly.ResetsAt.Equal(time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("monthly reset time not parsed: %#v", monthly.ResetsAt)
	}
	if _, ok := byID["usages-limit-month-code"]; !ok {
		t.Fatalf("monthly coding window missing: %#v", byID)
	}
	limit, ok := byID["limit-5h"]
	if !ok || limit.Used == nil || *limit.Used != 66 || limit.Limit == nil || *limit.Limit != 100 || limit.Remaining == nil || *limit.Remaining != 34 {
		t.Fatalf("5h limit window missing or wrong: %#v", limit)
	}
	if limit.UsedPercent == nil || *limit.UsedPercent != 66 {
		t.Fatalf("5h limit percent not derived from amounts: %#v", limit)
	}
}

func TestParseAcceptsLegacyShapes(t *testing.T) {
	data := []byte(`{"data": [
		{"model_name": "all", "used": 12, "limit": 200, "resetTime": "2026-10-01T00:00:00Z"},
		{"model_name": "kimi-k2", "used": 5, "limit": 50}
	]}`)
	windows, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 2 || windows[0].Label != "All models" || windows[1].Scope != "kimi-k2" {
		t.Fatalf("data list shape not parsed: %#v", windows)
	}
	legacy := []byte(`{"usage": {"used_ratio": 0.4, "reset_in": 3600}, "limits": [
		{"window": {"duration": 24, "timeUnit": "TIME_UNIT_HOUR"}, "detail": {"used": "3", "limit": "10"}}
	]}`)
	windows, err = Parse(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 2 {
		t.Fatalf("usage+limits shape not parsed: %#v", windows)
	}
	byID := make(map[string]domain.QuotaWindow, len(windows))
	for _, window := range windows {
		byID[window.ID] = window
	}
	usage, ok := byID["usages-usage"]
	if !ok || usage.UsedPercent == nil || *usage.UsedPercent != 40 {
		t.Fatalf("usage entry missing or wrong: %#v", usage)
	}
	if usage.ResetsAt == nil || time.Until(*usage.ResetsAt) < 55*time.Minute {
		t.Fatalf("reset_in not honored: %#v", usage.ResetsAt)
	}
	dayLimit, ok := byID["limit-1d"]
	if !ok || dayLimit.Limit == nil || *dayLimit.Limit != 10 {
		t.Fatalf("24h limit window missing or wrong: %#v", dayLimit)
	}
}

func TestParseRejectsInvalidJSON(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("expected invalid JSON to be rejected")
	}
}

func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	home := t.TempDir()
	cfg := config.KimiConfig{
		Enabled:       true,
		HomePaths:     []string{home},
		SettingsPaths: []string{filepath.Join(home, "claude-settings.json")},
	}
	return New(cfg)
}

func TestDiscoveryNeverExportsToken(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "super-secret-fixture-token")
	provider := newTestProvider(t)
	candidates, err := provider.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one candidate, got %#v", candidates)
	}
	payload, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "super-secret-fixture-token") {
		t.Fatal("credential escaped into a discovery candidate")
	}
}

func TestDiscoveryReadsCLICredentials(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(home, "credentials", "kimi-code-env-test.json")
	if err := os.WriteFile(credential, []byte(`{"access_token": "cli-oauth-token", "expires_at": 1790000000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.KimiConfig{Enabled: true, HomePaths: []string{home}, SettingsPaths: []string{filepath.Join(home, "absent.json")}}
	candidates, err := New(cfg).Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Source != "kimi-cli" {
		t.Fatalf("CLI credential not discovered: %#v", candidates)
	}
	payload, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "cli-oauth-token") {
		t.Fatal("credential escaped into a discovery candidate")
	}
}

func TestDiscoveryDeduplicatesSameTokenAcrossSources(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(home, "credentials", "kimi-code-env-test.json")
	if err := os.WriteFile(credential, []byte(`{"access_token": "same-token-value"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KIMI_API_KEY", "same-token-value")
	cfg := config.KimiConfig{Enabled: true, HomePaths: []string{home}, SettingsPaths: []string{filepath.Join(home, "absent.json")}}
	candidates, err := New(cfg).Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("same token across sources must deduplicate to one account: %#v", candidates)
	}
	if candidates[0].Source != "environment" {
		t.Fatalf("explicit environment source must win deduplication: %#v", candidates)
	}
}

func TestClaudeSettingsKimiBaseURLPromotesToken(t *testing.T) {
	home := t.TempDir()
	settingsPath := filepath.Join(home, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"env": {"ANTHROPIC_BASE_URL": "https://api.kimi.com", "ANTHROPIC_AUTH_TOKEN": "settings-kimi-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.KimiConfig{Enabled: true, HomePaths: []string{home}, SettingsPaths: []string{settingsPath}}
	candidates, err := New(cfg).Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Source != "claude-settings" {
		t.Fatalf("Kimi Claude settings entry not discovered: %#v", candidates)
	}
}

func TestResolveRereadsRotatedCredential(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(home, "credentials", "kimi-code-env-test.json")
	if err := os.WriteFile(credential, []byte(`{"access_token": "token-before-rotation"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	credentials := secret{Kind: "cli", Path: credential}
	token, err := credentials.resolve()
	if err != nil || token != "token-before-rotation" {
		t.Fatalf("first resolve: token=%q err=%v", token, err)
	}
	// The CLI rewrote the file with a fresh token; a fetch must pick it up.
	if err := os.WriteFile(credential, []byte(`{"access_token": "token-after-rotation"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err = credentials.resolve()
	if err != nil || token != "token-after-rotation" {
		t.Fatalf("resolve after rotation: token=%q err=%v", token, err)
	}
}

func TestRecognizedBaseURLRejectsUnrelatedAnthropicProxy(t *testing.T) {
	if recognizedBaseURL("https://example.com/api/anthropic") {
		t.Fatal("unrelated base URL must never promote ANTHROPIC_AUTH_TOKEN to a Kimi key")
	}
	if !recognizedBaseURL("https://api.kimi.com") {
		t.Fatal("expected official Kimi base URL")
	}
}
