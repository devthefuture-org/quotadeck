package control

import (
	"maps"
	"strconv"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

// WithModels selects the configured gateway models; New starts from the
// defaults shipped in config's models.yaml.
func (m *Manager) WithModels(models config.ModelsConfig) *Manager {
	m.models = models
	return m
}

// gatewayModelEnvironment routes the main model, every tier, sub-agents and
// the auto mode classifier to the configured model.
func gatewayModelEnvironment(model config.GatewayModel) map[string]string {
	tiers := model.Tiers
	env := map[string]string{
		"ANTHROPIC_MODEL":                model.Model,
		"ANTHROPIC_DEFAULT_FABLE_MODEL":  model.Tier(tiers.Fable),
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   model.Tier(tiers.Opus),
		"ANTHROPIC_DEFAULT_SONNET_MODEL": model.Tier(tiers.Sonnet),
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  model.Tier(tiers.Haiku),
		"ANTHROPIC_SMALL_FAST_MODEL":     model.Tier(tiers.Haiku),
		"CLAUDE_CODE_SUBAGENT_MODEL":     model.Tier(tiers.Subagent),
		"CLAUDE_CODE_AUTO_MODE_MODEL":    model.Tier(tiers.Classifier),
	}
	if model.ContextWindow > 0 {
		window := strconv.Itoa(model.ContextWindow)
		env["CLAUDE_CODE_AUTO_COMPACT_WINDOW"] = window
		env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = window
	}
	return env
}

// withModel merges a gateway's fixed settings with its model environment.
func withModel(fixed map[string]string, model config.GatewayModel) map[string]string {
	env := maps.Clone(fixed)
	maps.Copy(env, gatewayModelEnvironment(model))
	return env
}

// managedRecordKey stores, in a gateway's sidecar, the values its last
// activation wrote. Models come from configuration, so the values to undo
// may differ from what the current configuration would write.
const managedRecordKey = "quotadeck:managed"

func recordManaged(sidecar map[string]any, managed map[string]string) {
	recorded := make(map[string]any, len(managed))
	for name, value := range managed {
		recorded[name] = value
	}
	sidecar[managedRecordKey] = recorded
}

// rememberManaged updates the values recorded in a gateway's sidecar when it
// is selected again, keeping the preferences captured before.
func rememberManaged(path string, managed map[string]string) error {
	sidecar, err := readSettings(path)
	if err != nil {
		return err
	}
	recordManaged(sidecar, managed)
	return writeSettings(path, sidecar, 0o600)
}

// writtenByGateway reports whether value is one the gateway wrote for name,
// by its last recorded activation or by the current configuration.
func writtenByGateway(name string, value any, managed map[string]string, sidecar map[string]any) bool {
	if current, ok := managed[name]; ok && value == any(current) {
		return true
	}
	recorded, _ := sidecar[managedRecordKey].(map[string]any)
	if written, ok := recorded[name]; ok && value == written {
		return true
	}
	return false
}

// restoreManaged gives back the user's previous value, or removes the key,
// for every key the gateway wrote and the user left unchanged since.
func restoreManaged(environment map[string]any, managed map[string]string, sidecar map[string]any) {
	names := map[string]bool{}
	for name := range managed {
		names[name] = true
	}
	recorded, _ := sidecar[managedRecordKey].(map[string]any)
	for name := range recorded {
		names[name] = true
	}
	for name := range names {
		value, exists := environment[name]
		if !exists || !writtenByGateway(name, value, managed, sidecar) {
			continue
		}
		if previous, saved := sidecar[name]; saved {
			environment[name] = previous
		} else {
			delete(environment, name)
		}
	}
}

// planModelStatus reports the configured model and whether the active
// settings run it.
func planModelStatus(status *ZAIStatus, model config.GatewayModel) {
	status.ModelName = model.DisplayName()
	status.Selected = status.Active && status.Model == model.Model
}
