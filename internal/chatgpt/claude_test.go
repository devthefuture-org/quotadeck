package chatgpt

import (
	"reflect"
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

var preferences = config.DefaultModels().ChatGPT

const (
	AstraModel = "gpt-6.2-astra"
	LunaModel  = "gpt-6-luna"
)

func TestFableModelUsesPlanCatalog(t *testing.T) {
	const sol = "gpt-6.2-sol"
	withoutAstra := SelectClaudeModels([]Model{{Slug: sol}}, sol, "", preferences)
	if withoutAstra.Fable != sol || withoutAstra.Environment("endpoint")["CLAUDE_CODE_AUTO_MODE_MODEL"] != sol {
		t.Fatal("Fable and the classifier should use the default model when no alternatives are available")
	}
	selection := SelectClaudeModels([]Model{
		{Slug: sol, ContextWindow: 500_000}, {Slug: AstraModel, ContextWindow: 272_000},
		{Slug: LunaModel, ContextWindow: 272_000},
		{Slug: "unused-model", ContextWindow: 1_000_000},
	}, sol, "", preferences)
	env := selection.Environment("http://127.0.0.1:9211/chatgpt")
	if env["ANTHROPIC_MODEL"] != sol || env["ANTHROPIC_DEFAULT_FABLE_MODEL"] != AstraModel ||
		env["ANTHROPIC_DEFAULT_FABLE_MODEL_NAME"] != "GPT-6.2 Astra" {
		t.Fatal("Astra should be available through Fable while the default stays on Sol")
	}
	if env["CLAUDE_CODE_AUTO_MODE_MODEL"] != LunaModel || env["CLAUDE_CODE_AUTO_MODE_SERVER"] != "0" {
		t.Fatal("Claude Code should send Auto mode classifier requests to Luna")
	}
	if !reflect.DeepEqual(selection.ContextWindows, map[string]int{sol: 500_000, AstraModel: 272_000}) ||
		env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] != "500000" ||
		env["QUOTADECK_CHATGPT_CONTEXT_WINDOWS"] != `{"gpt-6.2-astra":272000,"gpt-6.2-sol":500000}` {
		t.Fatalf("Claude Code settings should use the windows of the configured models: %+v", selection)
	}
	withoutAstraWindow := SelectClaudeModels([]Model{{Slug: sol, ContextWindow: 500_000}, {Slug: AstraModel}}, sol, "", preferences)
	if _, ok := withoutAstraWindow.Environment("endpoint")["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; ok {
		t.Fatal("a shared hard cap would also constrain the model whose window is unknown")
	}
}

// The preferences come from configuration: a catalog offering the shipped
// defaults must not win over configured ones.
func TestClaudeModelsFollowConfiguredPreferences(t *testing.T) {
	const sol = "gpt-6.2-sol"
	configured := config.ChatGPTModels{Fable: config.ModelChoice{Name: "GPT-7 Nova", Model: "gpt-7-nova"}, Classifier: "gpt-7-mini"}
	selection := SelectClaudeModels([]Model{
		{Slug: sol}, {Slug: AstraModel}, {Slug: LunaModel}, {Slug: "gpt-7-nova", ContextWindow: 400_000}, {Slug: "gpt-7-mini"},
	}, sol, "", configured)
	env := selection.Environment("endpoint")
	if env["ANTHROPIC_DEFAULT_FABLE_MODEL"] != "gpt-7-nova" || env["ANTHROPIC_DEFAULT_FABLE_MODEL_NAME"] != "GPT-7 Nova" ||
		env["CLAUDE_CODE_AUTO_MODE_MODEL"] != "gpt-7-mini" || !reflect.DeepEqual(selection.ContextWindows, map[string]int{"gpt-7-nova": 400_000}) {
		t.Fatalf("configured preferences were not applied: %+v", env)
	}
}
