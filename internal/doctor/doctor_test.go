package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/provider/kimi"
)

// isolatedConfig returns defaults whose discovery paths point at a temporary
// directory, so tests never read the real ~/.claude or ~/.kimi-code state.
func isolatedConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := config.Default()
	cfg.Providers.ZAI.SettingsPaths = []string{filepath.Join(t.TempDir(), "absent-settings.json")}
	temp := t.TempDir()
	cfg.Providers.Kimi.SettingsPaths = []string{filepath.Join(temp, "absent-settings.json")}
	cfg.Providers.Kimi.HomePaths = []string{temp}
	return cfg
}

func TestReportContainsPresenceButNeverSecretValue(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "doctor-must-not-leak-this")
	cfg := isolatedConfig(t)
	cfg.Providers.ZAI.Accounts = nil
	report := (Collector{Config: cfg, ConfigPath: "/tmp/config.yaml", Version: "test"}).Collect(t.Context())
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "doctor-must-not-leak-this") {
		t.Fatal("doctor leaked a secret value")
	}
	if !strings.Contains(text, `"secretPresent":"true"`) {
		t.Fatalf("doctor should report presence metadata: %s", text)
	}
}

func TestDisabledProvidersNeverAcceptSources(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "present-but-disabled")
	cfg := isolatedConfig(t)
	cfg.Providers.Claude.Enabled = false
	cfg.Providers.ZAI.Enabled = false
	cfg.Providers.Codex.Enabled = false
	cfg.Providers.Kimi.Enabled = false

	report := (Collector{Config: cfg, Version: "test"}).Collect(t.Context())
	for _, source := range report.Sources {
		if source.Accepted {
			t.Fatalf("disabled provider %q accepted source %q", source.Provider, source.Source)
		}
		if source.Reason != "provider disabled" {
			t.Fatalf("disabled provider %q reported reason %q", source.Provider, source.Reason)
		}
	}
}

func TestManagedZAIKeyIsReportedWithExplicitAccounts(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "managed-doctor-secret")
	t.Setenv("TEAM_ZAI_KEY", "")
	cfg := isolatedConfig(t)
	cfg.Providers.ZAI.Accounts = []config.ZAIAccountConfig{{Label: "Team", KeyEnv: "TEAM_ZAI_KEY"}}

	report := (Collector{Config: cfg, Version: "test"}).Collect(t.Context())
	found := false
	for _, source := range report.Sources {
		if source.Provider == "zai" && source.Metadata["keyEnv"] == "ZAI_API_KEY" {
			found = source.Accepted && source.Metadata["secretPresent"] == "true"
		}
	}
	if !found {
		t.Fatal("doctor did not report the managed ZAI_API_KEY fallback")
	}
}

func TestKimiSourcesVerdicts(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "credentials", "cli.json"), []byte(`{"access_token": "kimi-doctor-fixture-token"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	decoyHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(decoyHome, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoyHome, "credentials", "decoy.json"), []byte(`{"account_id": "decoy-no-token-here"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	kimiSettings := t.TempDir()
	settingsPath := filepath.Join(kimiSettings, "settings.json")
	if err := os.WriteFile(settingsPath, []byte(`{"env": {"ANTHROPIC_BASE_URL": "https://api.kimi.com", "ANTHROPIC_AUTH_TOKEN": "kimi-settings-fixture-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelatedSettings := t.TempDir()
	unrelatedPath := filepath.Join(unrelatedSettings, "settings.json")
	if err := os.WriteFile(unrelatedPath, []byte(`{"env": {"ANTHROPIC_BASE_URL": "https://example.com/api/anthropic", "ANTHROPIC_AUTH_TOKEN": "unrelated-fixture-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := isolatedConfig(t)
	t.Setenv("KIMI_API_KEY", "kimi-env-fixture-token")
	cfg.Providers.Kimi.HomePaths = []string{home}
	cfg.Providers.Kimi.SettingsPaths = []string{settingsPath}

	report := (Collector{Config: cfg, Version: "test"}).Collect(t.Context())
	type sourceKey struct{ source, keyEnv string }
	verdicts := map[sourceKey]bool{}
	for _, source := range report.Sources {
		if source.Provider == "kimi" {
			verdicts[sourceKey{source.Source, source.Metadata["keyEnv"]}] = source.Accepted
		}
	}
	if !verdicts[sourceKey{"environment", "KIMI_API_KEY"}] {
		t.Fatalf("environment source with a key must be accepted: %#v", report.Sources)
	}
	if !verdicts[sourceKey{"claude-settings", ""}] {
		t.Fatalf("claude-settings with a Kimi base URL and a token must be accepted: %#v", report.Sources)
	}
	if !verdicts[sourceKey{"kimi-cli", ""}] {
		t.Fatalf("kimi-cli with a credential carrying an access_token must be accepted: %#v", report.Sources)
	}

	cfgDecoy := isolatedConfig(t)
	cfgDecoy.Providers.Kimi.HomePaths = []string{decoyHome}
	cfgDecoy.Providers.Kimi.SettingsPaths = []string{unrelatedPath}
	reportDecoy := (Collector{Config: cfgDecoy, Version: "test"}).Collect(t.Context())
	for _, source := range reportDecoy.Sources {
		if source.Provider != "kimi" || source.Source == "environment" {
			// KIMI_API_KEY stays set for the whole test, so the environment
			// source legitimately remains accepted in this second pass.
			continue
		}
		if source.Accepted {
			t.Fatalf("source %q must not be accepted: %+v", source.Source, source)
		}
	}
	if kimi.CredentialHasToken(filepath.Join(decoyHome, "credentials", "decoy.json")) {
		t.Fatal("a credential file without an access_token must not count as a credential")
	}
}
