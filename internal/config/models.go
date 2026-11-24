package config

import (
	_ "embed"
	"fmt"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

//go:embed models.yaml
var defaultModels []byte

// ModelsConfig lists the models QuotaDeck selects in Claude Code for each
// gateway plan. models.yaml holds the defaults; config.yaml overrides them
// field by field.
type ModelsConfig struct {
	ZAI      GatewayModel  `yaml:"zai"`
	Kimi     GatewayModel  `yaml:"kimi"`
	OpenCode GatewayModel  `yaml:"opencode"`
	ChatGPT  ChatGPTModels `yaml:"chatgpt"`
}

// GatewayModel is the model a fixed-model gateway serves to Claude Code.
type GatewayModel struct {
	Name          string     `yaml:"name"`
	Model         string     `yaml:"model"`
	ContextWindow int        `yaml:"contextWindow"`
	Tiers         ModelTiers `yaml:"tiers"`
}

// ModelTiers overrides the model for one Claude Code tier; empty uses Model.
type ModelTiers struct {
	Fable      string `yaml:"fable"`
	Opus       string `yaml:"opus"`
	Sonnet     string `yaml:"sonnet"`
	Haiku      string `yaml:"haiku"`
	Subagent   string `yaml:"subagent"`
	Classifier string `yaml:"classifier"`
}

// ChatGPTModels holds preferences applied when the plan's catalog offers them.
type ChatGPTModels struct {
	Fable      ModelChoice `yaml:"fable"`
	Classifier string      `yaml:"classifier"`
}

type ModelChoice struct {
	Name  string `yaml:"name"`
	Model string `yaml:"model"`
}

// modelOverride records which fields config.yaml sets for one model.
type modelOverride struct {
	Name  *string `yaml:"name"`
	Model *string `yaml:"model"`
}

// dropInheritedNames clears a default name whose model config.yaml replaced
// by another one without naming it: the name describes the default model, not
// the override. DisplayName then shows the overriding id.
func (c *ModelsConfig) dropInheritedNames(contents []byte) error {
	defaults := DefaultModels()
	var overrides struct {
		Models struct {
			ZAI      modelOverride `yaml:"zai"`
			Kimi     modelOverride `yaml:"kimi"`
			OpenCode modelOverride `yaml:"opencode"`
			ChatGPT  struct {
				Fable modelOverride `yaml:"fable"`
			} `yaml:"chatgpt"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal(contents, &overrides); err != nil {
		return err
	}
	for _, model := range []struct {
		override modelOverride
		standard string
		name     *string
	}{
		{overrides.Models.ZAI, defaults.ZAI.Model, &c.ZAI.Name},
		{overrides.Models.Kimi, defaults.Kimi.Model, &c.Kimi.Name},
		{overrides.Models.OpenCode, defaults.OpenCode.Model, &c.OpenCode.Name},
		{overrides.Models.ChatGPT.Fable, defaults.ChatGPT.Fable.Model, &c.ChatGPT.Fable.Name},
	} {
		if model.override.Model != nil && *model.override.Model != model.standard && model.override.Name == nil {
			*model.name = ""
		}
	}
	return nil
}

// DefaultModels returns the models shipped in models.yaml.
func DefaultModels() ModelsConfig {
	var models ModelsConfig
	if err := yaml.Unmarshal(defaultModels, &models); err != nil {
		panic(fmt.Sprintf("embedded models.yaml: %v", err))
	}
	return models
}

// Tier returns the model id for a tier, falling back to the main model.
func (m GatewayModel) Tier(id string) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	return m.Model
}

// DisplayName returns the configured name, or the model id without one.
func (m GatewayModel) DisplayName() string {
	if strings.TrimSpace(m.Name) != "" {
		return m.Name
	}
	return m.Model
}

// Claude Code compacts no earlier than 100K tokens and no later than 1M.
const (
	minContextWindow = 100_000
	maxContextWindow = 1_000_000
)

func (c ModelsConfig) Validate() error {
	for _, gateway := range []struct {
		field string
		model GatewayModel
	}{{"models.zai", c.ZAI}, {"models.kimi", c.Kimi}, {"models.opencode", c.OpenCode}} {
		if err := validateModelID(gateway.model.Model, gateway.field+".model", false); err != nil {
			return err
		}
		window := gateway.model.ContextWindow
		if window != 0 && (window < minContextWindow || window > maxContextWindow) {
			return fmt.Errorf("%s.contextWindow must be 0 or between %d and %d tokens, got %d", gateway.field, minContextWindow, maxContextWindow, window)
		}
		tiers := gateway.model.Tiers
		for name, id := range map[string]string{
			"fable": tiers.Fable, "opus": tiers.Opus, "sonnet": tiers.Sonnet,
			"haiku": tiers.Haiku, "subagent": tiers.Subagent, "classifier": tiers.Classifier,
		} {
			if err := validateModelID(id, gateway.field+".tiers."+name, true); err != nil {
				return err
			}
		}
	}
	if err := validateModelID(c.ChatGPT.Fable.Model, "models.chatgpt.fable.model", true); err != nil {
		return err
	}
	return validateModelID(c.ChatGPT.Classifier, "models.chatgpt.classifier", true)
}

// Model ids reach Claude Code settings verbatim: whitespace is always a typo.
func validateModelID(id, field string, optional bool) error {
	if id == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%s is required", field)
	}
	if strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("%s must not contain whitespace, got %q", field, id)
	}
	return nil
}
