package config

import (
	"strings"
	"testing"
)

func TestDefaultModelsShipCurrentGatewayModels(t *testing.T) {
	models := Default().Models
	if err := models.Validate(); err != nil {
		t.Fatalf("embedded models.yaml must validate: %v", err)
	}
	for name, got := range map[string]GatewayModel{"zai": models.ZAI, "kimi": models.Kimi, "opencode": models.OpenCode} {
		if got.Model == "" || got.Name == "" || got.ContextWindow == 0 {
			t.Fatalf("%s default is incomplete: %#v", name, got)
		}
	}
	if models.ZAI.Model != "glm-5.4[1m]" || models.ZAI.ContextWindow != 1_000_000 {
		t.Fatalf("Z.ai must default to GLM-5.4 with the 1M window: %#v", models.ZAI)
	}
	if models.ChatGPT.Fable.Model == "" || models.ChatGPT.Classifier == "" {
		t.Fatalf("ChatGPT preferences are missing: %#v", models.ChatGPT)
	}
}

// config.yaml overrides one field without erasing its siblings or the other
// gateways.
func TestLoadOverridesModelsFieldByField(t *testing.T) {
	path := writeConfig(t, "models:\n  zai:\n    model: glm-5.5\n    tiers:\n      haiku: glm-5.3-flash\n  chatgpt:\n    classifier: gpt-6-mini\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultModels()
	zai := cfg.Models.ZAI
	if zai.Model != "glm-5.5" || zai.Tiers.Haiku != "glm-5.3-flash" || zai.ContextWindow != defaults.ZAI.ContextWindow {
		t.Fatalf("partial Z.ai override = %#v", zai)
	}
	if zai.Tier(zai.Tiers.Haiku) != "glm-5.3-flash" || zai.Tier(zai.Tiers.Opus) != "glm-5.5" {
		t.Fatalf("tier fallback is wrong: %#v", zai.Tiers)
	}
	if cfg.Models.Kimi != defaults.Kimi || cfg.Models.OpenCode != defaults.OpenCode {
		t.Fatalf("untouched gateways must keep their defaults: %#v", cfg.Models)
	}
	if cfg.Models.ChatGPT.Classifier != "gpt-6-mini" || cfg.Models.ChatGPT.Fable != defaults.ChatGPT.Fable {
		t.Fatalf("partial ChatGPT override = %#v", cfg.Models.ChatGPT)
	}
}

func TestLoadRejectsInvalidModels(t *testing.T) {
	for _, tc := range []struct{ yaml, field string }{
		{"models:\n  kimi:\n    model: \"\"\n", "models.kimi.model"},
		{"models:\n  zai:\n    model: \"glm 5.4\"\n", "models.zai.model"},
		{"models:\n  opencode:\n    contextWindow: 5000\n", "models.opencode.contextWindow"},
		{"models:\n  zai:\n    contextWindow: 2000000\n", "models.zai.contextWindow"},
		{"models:\n  zai:\n    tiers:\n      haiku: \"a\\nb\"\n", "models.zai.tiers.haiku"},
		{"models:\n  chatgpt:\n    classifier: \"gpt 6\"\n", "models.chatgpt.classifier"},
	} {
		if _, err := Load(writeConfig(t, tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.field) {
			t.Fatalf("%q must be rejected on %s, got %v", tc.yaml, tc.field, err)
		}
	}
	if _, err := Load(writeConfig(t, "models:\n  zai:\n    contextWindow: 0\n")); err != nil {
		t.Fatalf("contextWindow 0 leaves the window to Claude Code: %v", err)
	}
}

// A default name describes the default model: an override that replaces the
// model without naming it shows its own id, never the previous model's name.
func TestModelOverrideWithoutNameDropsTheDefaultName(t *testing.T) {
	path := writeConfig(t, "models:\n  opencode:\n    model: next-flash\n  kimi:\n    model: k4\n    name: K4\n  zai:\n    model: glm-5.4[1m]\n  chatgpt:\n    fable:\n      model: gpt-7-nova\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	defaults := DefaultModels()
	if got := cfg.Models.OpenCode.DisplayName(); got != "next-flash" {
		t.Fatalf("an unnamed model override must show its id, got %q", got)
	}
	if cfg.Models.Kimi.DisplayName() != "K4" {
		t.Fatalf("an explicit name must be kept: %#v", cfg.Models.Kimi)
	}
	if cfg.Models.ZAI.Name != defaults.ZAI.Name {
		t.Fatalf("restating the default model keeps its name: %#v", cfg.Models.ZAI)
	}
	if cfg.Models.ChatGPT.Fable.Name != "" {
		t.Fatalf("an unnamed Fable override must not inherit %q", defaults.ChatGPT.Fable.Name)
	}
}
