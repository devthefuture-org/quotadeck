package chatgpt

import (
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

type ClaudeModels struct {
	Default        string
	Fable          string
	FableName      string
	Classifier     string
	ContextWindows map[string]int
}

func ClaudeEndpoint(bind string, port int) string {
	return "http://" + net.JoinHostPort(bind, strconv.Itoa(port)) + GatewayPath
}

// SelectClaudeModels applies the configured Fable and classifier preferences
// when the plan's catalog offers them; otherwise both use the default model.
func SelectClaudeModels(models []Model, defaultModel, classifierPreference string, preferences config.ChatGPTModels) ClaudeModels {
	selection := ClaudeModels{Default: defaultModel, Fable: defaultModel, Classifier: defaultModel, ContextWindows: map[string]int{}}
	fable := preferences.Fable.Model
	for _, model := range models {
		if fable != "" && model.Slug == fable {
			selection.Fable, selection.FableName = model.Slug, preferences.Fable.Name
		}
		if preferences.Classifier != "" && model.Slug == preferences.Classifier {
			selection.Classifier = model.Slug
		}
		if (model.Slug == defaultModel || fable != "" && model.Slug == fable) && model.ContextWindow >= 100_000 {
			selection.ContextWindows[model.Slug] = model.ContextWindow
		}
	}
	for _, model := range models {
		if model.Slug == classifierPreference {
			selection.Classifier = model.Slug
			break
		}
	}
	return selection
}

func (models ClaudeModels) Environment(endpoint string) map[string]string {
	model, fableModel := models.Default, models.Fable
	noProxy := os.Getenv("NO_PROXY")
	if noProxy == "" {
		noProxy = os.Getenv("no_proxy")
	}
	noProxy = strings.TrimPrefix(noProxy+",127.0.0.1,localhost,::1", ",")
	env := map[string]string{
		"ANTHROPIC_BASE_URL": endpoint, "ANTHROPIC_API_KEY": "",
		"ANTHROPIC_AUTH_TOKEN": "", "ANTHROPIC_CUSTOM_HEADERS": "",
		"CLAUDE_CODE_USE_BEDROCK": "", "CLAUDE_CODE_USE_VERTEX": "",
		"CLAUDE_CODE_USE_FOUNDRY": "", "CLAUDE_CODE_USE_ANTHROPIC_AWS": "",
		"ANTHROPIC_MODEL": model, "ANTHROPIC_SMALL_FAST_MODEL": model,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": model, "ANTHROPIC_DEFAULT_SONNET_MODEL": model,
		"ANTHROPIC_DEFAULT_OPUS_MODEL": model, "ANTHROPIC_DEFAULT_FABLE_MODEL": fableModel,
		"CLAUDE_CODE_SUBAGENT_MODEL":               model,
		"CLAUDE_CODE_AUTO_MODE_SERVER":             "0",
		"CLAUDE_CODE_AUTO_MODE_MODEL":              models.Classifier,
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
		"ENABLE_CLAUDEAI_MCP_SERVERS":              "false", "ENABLE_TOOL_SEARCH": "false",
		"NO_PROXY": noProxy, "no_proxy": noProxy,
	}
	if models.FableName != "" {
		env["ANTHROPIC_DEFAULT_FABLE_MODEL_NAME"] = models.FableName
	}
	if len(models.ContextWindows) > 0 {
		windows, _ := json.Marshal(models.ContextWindows)
		env["QUOTADECK_CHATGPT_CONTEXT_WINDOWS"] = string(windows)
	}
	// Claude Code only accepts one hard window per process. Leave room for both
	// aliases, and configure each model's auto-compact window separately.
	mainWindow, fableWindow := models.ContextWindows[model], models.ContextWindows[fableModel]
	if mainWindow > 0 && fableWindow > 0 {
		env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = strconv.Itoa(max(mainWindow, fableWindow))
	}
	return env
}
