package control

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/chatgpt"
)

func prepareChatGPT(t *testing.T, manager *Manager) string {
	t.Helper()
	database := filepath.Join(t.TempDir(), "user's state.db")
	dir := database + ".chatgpt"
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("G", 43)
	if err := os.WriteFile(filepath.Join(dir, "gateway.json"), mustJSON(key), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.WithChatGPT(chatgpt.ClaudeEndpoint("127.0.0.1", 9211), database)
	return key
}

func TestChatGPTActivationRestoresSettings(t *testing.T) {
	for _, next := range []string{"disconnect", "claude", "zai", "kimi", "opencode"} {
		t.Run(next, func(t *testing.T) {
			clearPlanKeys(t)
			run := &fakeRunner{}
			manager, paths := testManager(t, run)
			gatewayKey := prepareChatGPT(t, manager)
			if next == "opencode" {
				prepareOpenCode(t, manager)
			}
			original := map[string]any{
				"apiKeyHelper": "original-helper", "language": "French",
				"env": map[string]any{"ANTHROPIC_MODEL": "original-model", "ANTHROPIC_CUSTOM_HEADERS": "x-user: setting",
					"OTHER": "keep", "NO_PROXY": "intranet", claudeAiConnectorsSwitch: "true",
					"CLAUDE_CODE_AUTO_MODE_MODEL":    "original-classifier",
					"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "200000"},
				"modelSettings": map[string]any{
					"gpt-first":   map[string]any{},
					"gpt-second":  map[string]any{compactWindowSetting: "auto", "effortLevel": "high"},
					"claude-opus": map[string]any{"effortLevel": "high"},
				},
			}
			if err := writeSettings(paths.ClaudeSettings, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := manager.ConfigureChatGPT([]chatgpt.Model{{Slug: "gpt-first"}}, nil); err != nil {
				t.Fatal(err)
			}
			// Migrate the active plan from settings that predate per-model windows.
			catalog := []chatgpt.Model{
				{Slug: "gpt-first", ContextWindow: 500_000}, {Slug: chatgpt.AstraModel, ContextWindow: 272_000},
				{Slug: "gpt-second", ContextWindow: 600_000}, {Slug: chatgpt.LunaModel, ContextWindow: 272_000},
			}
			if err := manager.ConfigureChatGPT(catalog, nil); err != nil {
				t.Fatal(err)
			}
			migrated, _ := readSettings(paths.ClaudeSettings)
			migratedEnv := migrated["env"].(map[string]any)
			if migratedEnv["ANTHROPIC_MODEL"] != "gpt-first" || migratedEnv["ANTHROPIC_DEFAULT_FABLE_MODEL"] != chatgpt.AstraModel {
				t.Fatal("reapplying the active plan did not update the Fable alias")
			}
			if migratedEnv["CLAUDE_CODE_AUTO_MODE_MODEL"] != chatgpt.LunaModel || migratedEnv[autoModeServerSwitch] != "0" {
				t.Fatal("reapplying should route the classifier to Luna separately from the main model")
			}
			if got := migrated["modelSettings"].(map[string]any)["gpt-first"].(map[string]any)[compactWindowSetting]; got != float64(500_000) {
				t.Fatalf("legacy activation did not pick up its window: %v", got)
			}
			// Changing the default model must also preserve the pre-ChatGPT preferences.
			catalog[0], catalog[2] = catalog[2], catalog[0]
			if err := manager.ConfigureChatGPT(catalog, nil); err != nil {
				t.Fatal(err)
			}
			active, err := readSettings(paths.ClaudeSettings)
			if err != nil {
				t.Fatal(err)
			}
			env := active["env"].(map[string]any)
			modelSettings := active["modelSettings"].(map[string]any)
			if env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "600000" ||
				!reflect.DeepEqual(modelSettings["gpt-second"], map[string]any{compactWindowSetting: float64(600_000), "effortLevel": "high"}) ||
				!reflect.DeepEqual(modelSettings[chatgpt.AstraModel], map[string]any{compactWindowSetting: float64(272_000)}) ||
				!reflect.DeepEqual(modelSettings["gpt-first"], map[string]any{}) {
				t.Fatalf("unexpected auto-compact settings after reapplying: %+v", modelSettings)
			}
			helper, _ := active["apiKeyHelper"].(string)
			if env["ANTHROPIC_BASE_URL"] != manager.chatGPTEndpoint || env["ANTHROPIC_MODEL"] != "gpt-second" ||
				env["ANTHROPIC_API_KEY"] != "" || env["ANTHROPIC_AUTH_TOKEN"] != "" ||
				!strings.Contains(helper, "credential chatgpt --database") ||
				!strings.Contains(helper, `user'"'"'s state.db`) {
				t.Fatalf("ChatGPT did not configure Claude Code: %+v", active)
			}
			status := manager.Status(nil)
			if status.Mode != "chatgpt" || !status.ChatGPT.Active || status.ChatGPT.Model != "gpt-second" ||
				status.ChatGPT.FableModel != chatgpt.AstraModel {
				t.Fatalf("unexpected plan state: %+v", status)
			}
			if strings.Contains(string(mustJSON([]any{active, status})), gatewayKey) {
				t.Fatal("the gateway authentication key was written to settings or status")
			}
			backupInfo, err := os.Stat(manager.chatGPTPreferencesPath())
			if err != nil || backupInfo.Mode().Perm() != 0o600 {
				t.Fatal("the settings backup must be private")
			}

			// Simulate a daemon restart while the plan is selected.
			manager = New("cswap", run, paths).WithKimiHomes(manager.kimiHomes).
				WithOpenCode(manager.openCode).WithChatGPT(manager.chatGPTEndpoint, manager.chatGPTDatabase)
			switch next {
			case "disconnect":
				if err := manager.DeactivateChatGPT(); err != nil {
					t.Fatal(err)
				}
			case "claude":
				run.err = errors.New("cswap switch failed")
				if err := manager.SwitchClaude(t.Context(), "claude:cswap:1"); err == nil {
					t.Fatal("expected the cswap failure")
				}
				rolledBack, _ := readSettings(paths.ClaudeSettings)
				if !reflect.DeepEqual(rolledBack, active) {
					t.Fatal("the failed switch did not restore the active ChatGPT settings")
				}
				run.err = nil
			case "zai":
				if err := manager.ConfigureZAI(t.Context(), "zai-fixture", true); err != nil {
					t.Fatal(err)
				}
			case "kimi":
				if err := manager.ConfigureKimi(t.Context(), "kimi-fixture", true); err != nil {
					t.Fatal(err)
				}
			case "opencode":
				if err := manager.ConfigureOpenCode(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if manager.Status(nil).ChatGPT.Active && next != "claude" {
				t.Fatal("ChatGPT was left selected after switching")
			}
			if next != "disconnect" {
				if err := manager.SwitchClaude(t.Context(), "claude:cswap:1"); err != nil {
					t.Fatal(err)
				}
			}
			restored, err := readSettings(paths.ClaudeSettings)
			if err != nil || !reflect.DeepEqual(restored, original) {
				t.Fatalf("settings were not restored: %+v (%v)", restored, err)
			}
		})
	}
}

func TestChatGPTDisconnectKeepsSubsequentUserEdits(t *testing.T) {
	manager, paths := testManager(t, &fakeRunner{})
	prepareChatGPT(t, manager)
	if err := manager.ConfigureChatGPT([]chatgpt.Model{{Slug: "gpt-test", ContextWindow: 500_000}}, nil); err != nil {
		t.Fatal(err)
	}
	settings, err := readSettings(paths.ClaudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	settings["env"].(map[string]any)["ANTHROPIC_MODEL"] = "user-model"
	settings["apiKeyHelper"] = "user-helper"
	settings["modelSettings"].(map[string]any)["gpt-test"].(map[string]any)[compactWindowSetting] = 350_000
	if err := writeSettings(paths.ClaudeSettings, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeactivateChatGPT(); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"apiKeyHelper": "user-helper", "env": map[string]any{"ANTHROPIC_MODEL": "user-model"},
		"modelSettings": map[string]any{"gpt-test": map[string]any{compactWindowSetting: float64(350_000)}},
	}
	restored, err := readSettings(paths.ClaudeSettings)
	if err != nil || !reflect.DeepEqual(restored, want) {
		t.Fatalf("user edits were not preserved: %+v (%v)", restored, err)
	}
}

func TestChatGPTClassifierPreference(t *testing.T) {
	manager, paths := testManager(t, &fakeRunner{})
	prepareChatGPT(t, manager)
	const sol = "gpt-6.2-sol"
	alternate := "gpt-6.1-terra"
	catalog := []chatgpt.Model{{Slug: sol}, {Slug: chatgpt.AstraModel}, {Slug: chatgpt.LunaModel}, {Slug: alternate}}
	check := func(active bool, preference, classifier string) {
		t.Helper()
		status := manager.Status(nil).ChatGPT
		if status.Active != active || status.ClassifierPreference != preference || status.ClassifierModel != classifier ||
			(active && (status.Model != sol || status.FableModel != chatgpt.AstraModel)) {
			t.Fatalf("unexpected ChatGPT selection: %+v", status)
		}
	}
	if err := manager.ConfigureChatGPT(catalog, nil); err != nil {
		t.Fatal(err)
	}
	check(true, "", chatgpt.LunaModel)
	if err := manager.ConfigureChatGPT(catalog, &alternate); err != nil {
		t.Fatal(err)
	}
	check(true, alternate, alternate)
	unknown := "not-in-the-catalog"
	if err := manager.ConfigureChatGPT(catalog, &unknown); !errors.Is(err, ErrChatGPTClassifierUnknown) {
		t.Fatalf("unknown classifier should be rejected, got %v", err)
	}
	check(true, alternate, alternate)
	if err := manager.DeactivateChatGPT(); err != nil {
		t.Fatal(err)
	}
	manager = New("cswap", &fakeRunner{}, paths).WithChatGPT(manager.chatGPTEndpoint, manager.chatGPTDatabase)
	check(false, alternate, "")
	if err := manager.ConfigureChatGPT(catalog[:3], nil); err != nil {
		t.Fatal(err)
	}
	check(true, alternate, chatgpt.LunaModel)
	if err := manager.ConfigureChatGPT(catalog, nil); err != nil {
		t.Fatal(err)
	}
	check(true, alternate, alternate)
	defaultClassifier := ""
	if err := manager.ConfigureChatGPT(catalog, &defaultClassifier); err != nil {
		t.Fatal(err)
	}
	check(true, "", chatgpt.LunaModel)
}
