package control

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/domain"
)

func clearPlanKeys(t *testing.T) {
	t.Helper()
	for _, name := range []string{"KIMI_API_KEY", "KIMI_CODING_API_KEY", "ZAI_API_KEY"} {
		t.Setenv(name, "")
	}
}

func TestLocalKimiUsesRenewableHelperWithoutCopyingTokens(t *testing.T) {
	clearPlanKeys(t)
	manager, paths := testManager(t, &fakeRunner{})
	home := filepath.Join(t.TempDir(), "Kimi's home")
	if err := os.MkdirAll(filepath.Join(home, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[providers.kimi]\nbase_url = \"https://api.kimi.ai/coding/v1\"\n[providers.kimi.oauth]\nstorage = \"file\"\nkey = \"oauth/kimi-code-global\"\noauth_host = \"https://auth.kimi.ai\"\n"
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "local-access-fixture"
	body, _ := json.Marshal(map[string]any{"access_token": secret, "refresh_token": "local-refresh-fixture", "expires_at": time.Now().Add(time.Hour).Unix()})
	if err := os.WriteFile(filepath.Join(home, "credentials", "kimi-code-global.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	manager.WithKimiHomes([]string{home})
	original := map[string]any{"apiKeyHelper": "existing-helper", "env": map[string]any{"ANTHROPIC_MODEL": "opus"}}
	if err := writeSettings(paths.ClaudeSettings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	status := manager.Status(nil)
	if !status.Kimi.Configured || !status.Kimi.LocalAvailable || status.Kimi.Source != "kimi-cli" || status.Kimi.Endpoint != "https://api.kimi.ai/coding/" {
		t.Fatal("local login not available")
	}
	if err := manager.ConfigureKimi(t.Context(), "", true); err != nil {
		t.Fatal(err)
	}
	settings, _ := readSettings(paths.ClaudeSettings)
	env := settings["env"].(map[string]any)
	helper, _ := settings["apiKeyHelper"].(string)
	if !strings.Contains(helper, "credential kimi --home") || !strings.Contains(helper, `Kimi'"'"'s home`) || env["ANTHROPIC_BASE_URL"] != "https://api.kimi.ai/coding/" || env["ANTHROPIC_API_KEY"] != "" || env["ANTHROPIC_AUTH_TOKEN"] != "" {
		t.Fatal("incorrect helper configuration")
	}
	for _, path := range []string{paths.ClaudeSettings, paths.Environment, manager.kimiPreferencesPath()} {
		data, _ := os.ReadFile(path)
		if strings.Contains(string(data), secret) || strings.Contains(string(data), "local-refresh-fixture") {
			t.Fatal("OAuth credential copied out of the Kimi store")
		}
	}
	status = manager.Status(nil)
	if status.Mode != "kimi" || !status.Kimi.Active || status.Kimi.Source != "kimi-cli" {
		t.Fatal("local Kimi was not selected")
	}
	if err := manager.SwitchClaude(t.Context(), "claude:cswap:1"); err != nil {
		t.Fatal(err)
	}
	restored, _ := readSettings(paths.ClaudeSettings)
	if !reflect.DeepEqual(restored, original) {
		t.Fatal("original helper and model were not restored")
	}
	if err := manager.ConfigureKimi(t.Context(), "", true); err != nil {
		t.Fatal(err)
	}
	if err := manager.ConfigureZAI(t.Context(), "zai-local-fixture", true); err != nil {
		t.Fatal(err)
	}
	settings, _ = readSettings(paths.ClaudeSettings)
	if settings["apiKeyHelper"] != "existing-helper" || kimiHelperActive(settings) {
		t.Fatal("Kimi helper leaked into Z.ai selection")
	}
}

func TestKimiSaveActivateAndReturnToClaude(t *testing.T) {
	clearPlanKeys(t)
	commandRunner := &fakeRunner{}
	manager, paths := testManager(t, commandRunner)
	original := map[string]any{"permissions": map[string]any{"defaultMode": "plan"}, "env": map[string]any{
		"ANTHROPIC_MODEL": "opus", "KEEP_ME": "yes", "CLAUDE_CODE_EFFORT_LEVEL": "medium",
	}}
	if err := writeSettings(paths.ClaudeSettings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	secret := "kimi-control-fixture-key"
	if err := manager.ConfigureKimi(t.Context(), secret, false); err != nil {
		t.Fatal(err)
	}
	saved, _ := readSettings(paths.ClaudeSettings)
	if !reflect.DeepEqual(saved, original) {
		t.Fatal("saving a key changed the selected plan")
	}
	// A new manager can activate the persisted key without the process env.
	t.Setenv("KIMI_API_KEY", "")
	manager = New("cswap", commandRunner, paths)
	if err := manager.ConfigureKimi(t.Context(), "", true); err != nil {
		t.Fatal(err)
	}
	settings, _ := readSettings(paths.ClaudeSettings)
	env := settings["env"].(map[string]any)
	if env["ANTHROPIC_API_KEY"] != secret || env["ANTHROPIC_AUTH_TOKEN"] != nil || env["ANTHROPIC_BASE_URL"] != kimiEndpoint {
		t.Fatal("Kimi routing was not installed")
	}
	for _, name := range []string{"ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL"} {
		if env[name] != "k3" {
			t.Fatalf("%s must select K3", name)
		}
	}
	status := manager.Status(nil)
	encoded, _ := json.Marshal(status)
	if status.Mode != "kimi" || !status.Kimi.Active || !status.Kimi.Configured || status.Kimi.Model != "k3" || strings.Contains(string(encoded), secret) {
		t.Fatal("incorrect or unredacted Kimi status")
	}
	for _, path := range []string{paths.Environment, paths.ClaudeSettings, manager.kimiPreferencesPath()} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("configuration is not private: %s", path)
		}
	}
	// Reactivating must not replace the original model preferences with K3.
	if err := manager.ConfigureKimi(t.Context(), "", true); err != nil {
		t.Fatal(err)
	}
	if err := manager.SwitchClaude(t.Context(), "claude:cswap:2"); err != nil {
		t.Fatal(err)
	}
	restored, _ := readSettings(paths.ClaudeSettings)
	if !reflect.DeepEqual(restored, original) {
		t.Fatalf("original Claude preferences were not restored: %#v", restored)
	}
	status = manager.Status([]domain.AccountState{{Account: domain.Account{ID: "claude:cswap:2", ProviderID: "claude", Active: true}}})
	if status.Mode != "claude" || status.Kimi.Active || !status.Kimi.Configured {
		t.Fatalf("Kimi should remain configured after switching: %#v", status)
	}
}

func TestKimiAndZAIKeepKeysAcrossSwitches(t *testing.T) {
	for _, initial := range []string{"kimi", "zai"} {
		t.Run(initial, func(t *testing.T) {
			clearPlanKeys(t)
			manager, paths := testManager(t, &fakeRunner{})
			env := map[string]any{"KEEP_ME": "yes"}
			if initial == "kimi" {
				env["ANTHROPIC_BASE_URL"], env["ANTHROPIC_API_KEY"] = kimiEndpoint, "kimi-existing-fixture"
				env["ANTHROPIC_MODEL"] = "k3"
			} else {
				env["ANTHROPIC_BASE_URL"], env["ANTHROPIC_AUTH_TOKEN"] = zaiEndpoint, "zai-existing-fixture"
			}
			if err := writeSettings(paths.ClaudeSettings, map[string]any{"env": env}, 0o600); err != nil {
				t.Fatal(err)
			}
			if initial == "kimi" {
				if err := manager.ConfigureZAI(t.Context(), "zai-existing-fixture", true); err != nil {
					t.Fatal(err)
				}
			} else if err := manager.ConfigureKimi(t.Context(), "kimi-existing-fixture", true); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := manager.ConfigureKimi(t.Context(), "", true); err != nil {
					t.Fatal(err)
				}
				if err := manager.ConfigureZAI(t.Context(), "", true); err != nil {
					t.Fatal(err)
				}
			}
			settings, _ := readSettings(paths.ClaudeSettings)
			env = settings["env"].(map[string]any)
			if env["ANTHROPIC_BASE_URL"] != zaiEndpoint || env["ANTHROPIC_AUTH_TOKEN"] != "zai-existing-fixture" || env["ANTHROPIC_API_KEY"] != nil || env["ANTHROPIC_MODEL"] != nil || env["KEEP_ME"] != "yes" {
				t.Fatal("switching left Kimi settings in the Z.ai plan")
			}
			status := manager.Status(nil)
			if status.Mode != "zai" || status.Kimi.Active || !status.Kimi.Configured || !status.ZAI.Configured {
				t.Fatalf("unexpected plan status: %#v", status)
			}
		})
	}
}

func TestKimiSwitchFailureRestoresSettings(t *testing.T) {
	clearPlanKeys(t)
	manager, paths := testManager(t, &fakeRunner{err: errors.New("switch failed")})
	if err := manager.ConfigureKimi(t.Context(), "kimi-rollback-fixture", true); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(paths.ClaudeSettings)
	if err := manager.SwitchClaude(t.Context(), "claude:cswap:1"); err == nil {
		t.Fatal("expected switch failure")
	}
	restored, _ := os.ReadFile(paths.ClaudeSettings)
	if string(restored) != string(original) || manager.Status(nil).Mode != "kimi" {
		t.Fatal("failed switch did not restore Kimi")
	}
}

func TestKimiCredentialSourcesAndValidation(t *testing.T) {
	clearPlanKeys(t)
	manager, paths := testManager(t, &fakeRunner{})
	if err := manager.ConfigureKimi(t.Context(), "", true); !errors.Is(err, ErrKimiNotConfigured) {
		t.Fatalf("missing key: %v", err)
	}
	for _, value := range []string{"short", "valid-prefix\nINJECTED=value", "has a space"} {
		if err := manager.ConfigureKimi(t.Context(), value, true); !errors.Is(err, ErrInvalidKimiKey) {
			t.Fatalf("expected invalid key: %v", err)
		}
	}
	if err := writeSettings(paths.ClaudeSettings, map[string]any{"env": map[string]any{
		"ANTHROPIC_BASE_URL": "https://proxy.example.com/coding", "ANTHROPIC_API_KEY": "unrelated-key",
	}}, 0o600); err != nil {
		t.Fatal(err)
	}
	if manager.Status(nil).Kimi.Configured {
		t.Fatal("unrelated proxy classified as Kimi")
	}
	t.Setenv("KIMI_CODING_API_KEY", "kimi-alias-fixture")
	if !manager.Status(nil).Kimi.Configured {
		t.Fatal("KIMI_CODING_API_KEY not detected")
	}
	if err := manager.ConfigureKimi(t.Context(), "", true); err != nil {
		t.Fatal(err)
	}
}
