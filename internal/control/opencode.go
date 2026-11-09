package control

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/provider/opencode"
)

const openCodeMarker = "QUOTADECK_OPENCODE_AUTH"

var openCodeEnvironment = map[string]string{
	"ANTHROPIC_MODEL":                   opencode.Model,
	"ANTHROPIC_SMALL_FAST_MODEL":        opencode.Model,
	"ANTHROPIC_DEFAULT_FABLE_MODEL":     opencode.Model,
	"ANTHROPIC_DEFAULT_OPUS_MODEL":      opencode.Model,
	"ANTHROPIC_DEFAULT_SONNET_MODEL":    opencode.Model,
	"ANTHROPIC_DEFAULT_HAIKU_MODEL":     opencode.Model,
	"CLAUDE_CODE_SUBAGENT_MODEL":        opencode.Model,
	"CLAUDE_CODE_AUTO_MODE_SERVER":      "0",
	"CLAUDE_CODE_AUTO_MODE_MODEL":       opencode.Model,
	"CLAUDE_CODE_EFFORT_LEVEL":          "high",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW":   "1000000",
	"CLAUDE_CODE_MAX_CONTEXT_TOKENS":    "1000000",
	"CLAUDE_CODE_API_KEY_HELPER_TTL_MS": "60000",
}

func (m *Manager) WithOpenCode(cfg config.OpenCodeConfig) *Manager { m.openCode = cfg; return m }
func (m *Manager) localOpenCode() (opencode.Credential, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	credentials, err := opencode.LocalCredentials(ctx, m.openCode)
	if err != nil || len(credentials) == 0 {
		return opencode.Credential{}, false
	}
	return credentials[0], true
}
func openCodeActive(settings map[string]any) bool {
	env, _ := settings["env"].(map[string]any)
	helper, _ := settings["apiKeyHelper"].(string)
	return env["ANTHROPIC_BASE_URL"] == opencode.Endpoint && env[openCodeMarker] == "opencode-v2" && helper != ""
}
func (m *Manager) ConfigureOpenCode(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	credential, ok := m.localOpenCode()
	if !ok {
		return opencode.ErrLoginRequired
	}
	if _, err := credential.Token(ctx); err != nil {
		return err
	}
	binary, err := m.runner.LookPath("quotadeck")
	if err != nil {
		return fmt.Errorf("quotadeck CLI is required for OpenCode authentication")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	helper := quote(binary) + " credential opencode --database " + quote(credential.Database) + " --state " + quote(credential.State) + " --id " + quote(credential.ID)
	settings, err := readSettings(m.paths.ClaudeSettings)
	if err != nil {
		return err
	}
	if err = m.preservePlanKeys(settings); err != nil {
		return err
	}
	if _, err = m.disableKimi(settings); err != nil {
		return err
	}
	disableZAI(settings)
	environment, err := settingsEnvironment(settings)
	if err != nil {
		return err
	}
	if !openCodeActive(settings) {
		previous := map[string]any{}
		for key := range openCodeEnvironment {
			if v, ok := environment[key]; ok {
				previous[key] = v
			}
		}
		if v, ok := environment["ANTHROPIC_CUSTOM_HEADERS"]; ok {
			previous["ANTHROPIC_CUSTOM_HEADERS"] = v
		}
		if v, ok := settings["apiKeyHelper"]; ok {
			previous[previousHelperKey] = v
		}
		if err = writeSettings(m.openCodePreferencesPath(), previous, 0o600); err != nil {
			return err
		}
	}
	for key, v := range openCodeEnvironment {
		environment[key] = v
	}
	environment["ANTHROPIC_BASE_URL"] = opencode.Endpoint
	environment["ANTHROPIC_API_KEY"] = ""
	environment["ANTHROPIC_AUTH_TOKEN"] = ""
	environment[openCodeMarker] = "opencode-v2"
	environment["ANTHROPIC_CUSTOM_HEADERS"] = "x-opencode-org-id: " + credential.OrgID
	settings["apiKeyHelper"] = helper
	return writeSettings(m.paths.ClaudeSettings, settings, 0o600)
}
func (m *Manager) openCodePreferencesPath() string {
	return m.paths.ClaudeSettings + ".quotadeck-opencode.json"
}
func (m *Manager) disableOpenCode(settings map[string]any) (bool, error) {
	if !openCodeActive(settings) {
		return false, nil
	}
	previous, err := readSettings(m.openCodePreferencesPath())
	if err != nil {
		return false, err
	}
	environment, _ := settingsEnvironment(settings)
	for key, v := range openCodeEnvironment {
		if environment[key] != v {
			continue
		}
		if old, ok := previous[key]; ok {
			environment[key] = old
		} else {
			delete(environment, key)
		}
	}
	if v, ok := previous[previousHelperKey]; ok {
		settings["apiKeyHelper"] = v
	} else {
		delete(settings, "apiKeyHelper")
	}
	if v, ok := previous["ANTHROPIC_CUSTOM_HEADERS"]; ok {
		environment["ANTHROPIC_CUSTOM_HEADERS"] = v
	} else {
		delete(environment, "ANTHROPIC_CUSTOM_HEADERS")
	}
	for _, key := range []string{openCodeMarker, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"} {
		delete(environment, key)
	}
	return true, nil
}
