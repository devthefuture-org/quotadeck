package zai

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

func TestParsePreservesEveryLimit(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "zai", "dynamic-limits.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, windows, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	if plan != "GLM Coding Pro" || len(windows) != 3 {
		t.Fatalf("unexpected parse result plan=%q windows=%#v", plan, windows)
	}
	if windows[0].UsedPercent == nil || *windows[0].UsedPercent != 42 {
		t.Fatalf("expected normalized usage, got %#v", windows[0])
	}
	if windows[2].Kind != "mcp" {
		t.Fatalf("TIME_LIMIT should remain a distinct MCP lane: %#v", windows[2])
	}
}

func TestRecognizedBaseURLRejectsUnrelatedAnthropicProxy(t *testing.T) {
	if recognizedBaseURL("https://example.com/api/anthropic") {
		t.Fatal("unrelated base URL must never promote ANTHROPIC_AUTH_TOKEN to a Z.ai key")
	}
	if !recognizedBaseURL("https://api.z.ai/api/anthropic") {
		t.Fatal("expected official Z.ai base URL")
	}
}

func TestDiscoveryNeverExportsToken(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "super-secret-fixture-token")
	provider := New(config.ZAIConfig{Enabled: true})
	candidates, err := provider.Discover(t.Context())
	if err != nil || len(candidates) == 0 {
		t.Fatalf("discover candidates: %#v err=%v", candidates, err)
	}
	payload, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "super-secret-fixture-token") {
		t.Fatal("credential escaped into a discovery candidate")
	}
}

func TestDiscoverIncludesManagedDefaultAlongsideExplicitAccounts(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "managed-default-token")
	t.Setenv("MISSING_EXPLICIT_KEY", "")
	cfg := config.Default().Providers.ZAI
	cfg.Accounts = []config.ZAIAccountConfig{{Label: "Unavailable team", KeyEnv: "MISSING_EXPLICIT_KEY"}}

	accounts, err := New(cfg).Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].SourceMeta["keyEnv"] != "ZAI_API_KEY" {
		t.Fatalf("expected UI-managed ZAI_API_KEY fallback, got %#v", accounts)
	}
}
