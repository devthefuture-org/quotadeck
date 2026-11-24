package control

import (
	"reflect"
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

type gatewayCase struct {
	name     string
	model    func(*config.ModelsConfig) *config.GatewayModel
	status   func(Status) ZAIStatus
	activate func(*testing.T, *Manager)
}

func gatewayCases() []gatewayCase {
	return []gatewayCase{
		{"zai", func(c *config.ModelsConfig) *config.GatewayModel { return &c.ZAI }, func(s Status) ZAIStatus { return s.ZAI },
			func(t *testing.T, m *Manager) {
				if err := m.ConfigureZAI(t.Context(), "zai-models-fixture", true); err != nil {
					t.Fatal(err)
				}
			}},
		{"kimi", func(c *config.ModelsConfig) *config.GatewayModel { return &c.Kimi }, func(s Status) ZAIStatus { return s.Kimi.ZAIStatus },
			func(t *testing.T, m *Manager) {
				if err := m.ConfigureKimi(t.Context(), "kimi-models-fixture", true); err != nil {
					t.Fatal(err)
				}
			}},
		{"opencode", func(c *config.ModelsConfig) *config.GatewayModel { return &c.OpenCode }, func(s Status) ZAIStatus { return s.OpenCode.ZAIStatus },
			func(t *testing.T, m *Manager) {
				if err := m.ConfigureOpenCode(t.Context()); err != nil {
					t.Fatal(err)
				}
			}},
	}
}

func gatewayManager(t *testing.T) (*Manager, Paths, map[string]any) {
	t.Helper()
	clearPlanKeys(t)
	manager, paths := testManager(t, &fakeRunner{})
	prepareOpenCode(t, manager)
	original := map[string]any{"env": map[string]any{"KEEP_ME": "yes", "ANTHROPIC_MODEL": "opus"}}
	if err := writeSettings(paths.ClaudeSettings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	return manager, paths, original
}

func TestGatewaysSelectConfiguredModels(t *testing.T) {
	for _, gateway := range gatewayCases() {
		t.Run(gateway.name, func(t *testing.T) {
			manager, paths, original := gatewayManager(t)
			models := config.DefaultModels()
			*gateway.model(&models) = config.GatewayModel{
				Name: "Custom", Model: "custom-main",
				Tiers: config.ModelTiers{Haiku: "custom-fast", Classifier: "custom-judge"},
			}
			manager.WithModels(models)
			gateway.activate(t, manager)
			settings, _ := readSettings(paths.ClaudeSettings)
			env := settings["env"].(map[string]any)
			want := map[string]string{
				"ANTHROPIC_MODEL": "custom-main", "ANTHROPIC_DEFAULT_FABLE_MODEL": "custom-main",
				"ANTHROPIC_DEFAULT_OPUS_MODEL": "custom-main", "ANTHROPIC_DEFAULT_SONNET_MODEL": "custom-main",
				"ANTHROPIC_DEFAULT_HAIKU_MODEL": "custom-fast", "ANTHROPIC_SMALL_FAST_MODEL": "custom-fast",
				"CLAUDE_CODE_SUBAGENT_MODEL": "custom-main", "CLAUDE_CODE_AUTO_MODE_MODEL": "custom-judge",
			}
			for name, value := range want {
				if env[name] != value {
					t.Fatalf("%s = %#v, want %q", name, env[name], value)
				}
			}
			for _, name := range []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "CLAUDE_CODE_MAX_CONTEXT_TOKENS"} {
				if _, set := env[name]; set {
					t.Fatalf("contextWindow 0 must leave %s to Claude Code", name)
				}
			}
			if status := gateway.status(manager.Status(nil)); !status.Selected || status.ModelName != "Custom" || status.Model != "custom-main" {
				t.Fatalf("status must report the configured model as selected: %#v", status)
			}
			if err := manager.SwitchClaude(t.Context(), "claude:cswap:2"); err != nil {
				t.Fatal(err)
			}
			if restored, _ := readSettings(paths.ClaudeSettings); !reflect.DeepEqual(restored, original) {
				t.Fatalf("preferences not restored: %#v", restored)
			}
		})
	}
}

// A configuration change while a gateway is active (a new default after an
// upgrade, an edited config.yaml) must still undo what the activation wrote.
func TestGatewayModelChangeWhileActiveIsUndone(t *testing.T) {
	for _, gateway := range gatewayCases() {
		t.Run(gateway.name, func(t *testing.T) {
			manager, paths, original := gatewayManager(t)
			gateway.activate(t, manager)
			models := config.DefaultModels()
			*gateway.model(&models) = config.GatewayModel{Name: "Next", Model: "next-model"}
			manager.WithModels(models)
			if status := gateway.status(manager.Status(nil)); !status.Active || status.Selected || status.ModelName != "Next" {
				t.Fatalf("an activation on another model must not count as selected: %#v", status)
			}
			if err := manager.SwitchClaude(t.Context(), "claude:cswap:2"); err != nil {
				t.Fatal(err)
			}
			if restored, _ := readSettings(paths.ClaudeSettings); !reflect.DeepEqual(restored, original) {
				t.Fatalf("the previous model's settings leaked into Claude: %#v", restored)
			}
		})
	}
}
