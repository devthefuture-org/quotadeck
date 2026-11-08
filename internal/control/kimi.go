package control

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devthefuture-org/quotadeck/internal/provider/kimi"
)

const kimiEndpoint = "https://api.kimi.com/coding/"

// Route every Claude Code model tier to K3. The 256K cap also works with
// membership tiers whose K3 requests do not support the full 1M context.
// https://www.kimi.com/code/docs/en/third-party-tools/claude-code.html
// Auto mode's gateway classifier also runs on K3: the Kimi endpoint returns
// no verdict for the server-side review (see autoModeServerSwitch).
var kimiEnvironment = map[string]string{
	"ANTHROPIC_MODEL":                   "k3",
	"ANTHROPIC_SMALL_FAST_MODEL":        "k3",
	"ANTHROPIC_DEFAULT_FABLE_MODEL":     "k3",
	"ANTHROPIC_DEFAULT_OPUS_MODEL":      "k3",
	"ANTHROPIC_DEFAULT_SONNET_MODEL":    "k3",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL":     "k3",
	"CLAUDE_CODE_SUBAGENT_MODEL":        "k3",
	"CLAUDE_CODE_AUTO_MODE_SERVER":      "0",
	"CLAUDE_CODE_AUTO_MODE_MODEL":       "k3",
	"CLAUDE_CODE_EFFORT_LEVEL":          "high",
	"CLAUDE_CODE_AUTO_COMPACT_WINDOW":   "262144",
	"CLAUDE_CODE_MAX_CONTEXT_TOKENS":    "262144",
	"CLAUDE_CODE_API_KEY_HELPER_TTL_MS": "60000",
}

const kimiAuthMarker = "QUOTADECK_KIMI_AUTH"
const previousHelperKey = "quotadeck:apiKeyHelper"

func kimiHelperActive(settings map[string]any) bool {
	environment, _ := settings["env"].(map[string]any)
	baseURL, _ := environment["ANTHROPIC_BASE_URL"].(string)
	helper, _ := settings["apiKeyHelper"].(string)
	return kimi.RecognizedBaseURL(baseURL) && environment[kimiAuthMarker] == "kimi-cli" && helper != ""
}

func (m *Manager) localKimi() (kimi.LocalCredential, bool) {
	credentials := kimi.LocalCredentials(m.kimiHomes)
	if len(credentials) == 0 {
		return kimi.LocalCredential{}, false
	}
	return credentials[0], true
}

func (m *Manager) kimiHelper(credential kimi.LocalCredential) (string, error) {
	binary, err := m.runner.LookPath("quotadeck")
	if err != nil {
		return "", fmt.Errorf("quotadeck CLI is required for local Kimi authentication")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return "", err
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	return quote(binary) + " credential kimi --home " + quote(credential.Home) + " --name " + quote(credential.Name), nil
}

func kimiSettings(settings map[string]any) (bool, string) {
	environment, _ := settings["env"].(map[string]any)
	baseURL, _ := environment["ANTHROPIC_BASE_URL"].(string)
	if !kimi.RecognizedBaseURL(baseURL) {
		return false, ""
	}
	authToken, _ := environment["ANTHROPIC_AUTH_TOKEN"].(string)
	apiKey, _ := environment["ANTHROPIC_API_KEY"].(string)
	key := firstNonEmpty(authToken, apiKey)
	return key != "", key
}

func (m *Manager) kimiKey(settingsKey string) string {
	return firstNonEmpty(
		readEnvironmentValue(m.paths.Environment, "KIMI_API_KEY"),
		readEnvironmentValue(m.paths.Environment, "KIMI_CODING_API_KEY"),
		os.Getenv("KIMI_API_KEY"), os.Getenv("KIMI_CODING_API_KEY"), settingsKey,
	)
}

func (m *Manager) ConfigureKimi(ctx context.Context, apiKey string, activate bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		if err := validateAPIKey(apiKey); err != nil {
			return ErrInvalidKimiKey
		}
		if err := writeEnvironmentValue(m.paths.Environment, "KIMI_API_KEY", apiKey); err != nil {
			return fmt.Errorf("store Kimi configuration: %w", err)
		}
		if err := os.Setenv("KIMI_API_KEY", apiKey); err != nil {
			return fmt.Errorf("activate Kimi configuration: %w", err)
		}
	}
	settings, err := readSettings(m.paths.ClaudeSettings)
	if err != nil {
		return fmt.Errorf("read Claude settings: %w", err)
	}
	_, settingsKey := kimiSettings(settings)
	effectiveKey := firstNonEmpty(apiKey, m.kimiKey(settingsKey))
	endpoint, helper := kimiEndpoint, ""
	if effectiveKey == "" {
		credential, available := m.localKimi()
		if !available {
			return ErrKimiNotConfigured
		}
		endpoint = credential.Endpoint()
		helper, err = m.kimiHelper(credential)
		if err != nil {
			return err
		}
		if activate {
			if _, err := credential.Token(ctx); err != nil {
				return err
			}
		}
	}
	if !activate {
		return nil
	}
	if err := m.preservePlanKeys(settings); err != nil {
		return err
	}
	environment, err := settingsEnvironment(settings)
	if err != nil {
		return err
	}
	baseURL, _ := environment["ANTHROPIC_BASE_URL"].(string)
	if !kimi.RecognizedBaseURL(baseURL) {
		// Persist only the model preferences we override, so returning to
		// Claude/Z.ai restores them even after a daemon restart.
		previous := map[string]any{}
		for name := range kimiEnvironment {
			if value, exists := environment[name]; exists {
				previous[name] = value
			}
		}
		if value, exists := settings["apiKeyHelper"]; exists {
			previous[previousHelperKey] = value
		}
		if err := writeSettings(m.kimiPreferencesPath(), previous, 0o600); err != nil {
			return fmt.Errorf("preserve Claude model preferences: %w", err)
		}
	}
	disableZAI(settings)
	environment, _ = settingsEnvironment(settings)
	delete(environment, "ANTHROPIC_AUTH_TOKEN")
	if helper != "" {
		// Empty settings values override keys inherited from the launching
		// shell, which otherwise take precedence over apiKeyHelper.
		environment["ANTHROPIC_API_KEY"] = ""
		environment["ANTHROPIC_AUTH_TOKEN"] = ""
		environment[kimiAuthMarker] = "kimi-cli"
		settings["apiKeyHelper"] = helper
	} else {
		if kimiHelperActive(settings) {
			if err := m.restoreKimiHelper(settings); err != nil {
				return err
			}
		}
		delete(environment, kimiAuthMarker)
		environment["ANTHROPIC_API_KEY"] = effectiveKey
	}
	environment["ANTHROPIC_BASE_URL"] = endpoint
	for name, value := range kimiEnvironment {
		environment[name] = value
	}
	if err := writeSettings(m.paths.ClaudeSettings, settings, 0o600); err != nil {
		return fmt.Errorf("configure Claude Code for Kimi: %w", err)
	}
	return nil
}

func (m *Manager) restoreKimiHelper(settings map[string]any) error {
	previous, err := readSettings(m.kimiPreferencesPath())
	if err != nil {
		return fmt.Errorf("read Claude credential preferences: %w", err)
	}
	if value, exists := previous[previousHelperKey]; exists {
		settings["apiKeyHelper"] = value
	} else {
		delete(settings, "apiKeyHelper")
	}
	return nil
}

func (m *Manager) kimiPreferencesPath() string {
	return m.paths.ClaudeSettings + ".quotadeck-models.json"
}

func (m *Manager) disableKimi(settings map[string]any) (bool, error) {
	environment, _ := settings["env"].(map[string]any)
	baseURL, _ := environment["ANTHROPIC_BASE_URL"].(string)
	if !kimi.RecognizedBaseURL(baseURL) {
		return false, nil
	}
	previous, err := readSettings(m.kimiPreferencesPath())
	if err != nil {
		return false, fmt.Errorf("read Claude model preferences: %w", err)
	}
	if kimiHelperActive(settings) {
		if err := m.restoreKimiHelper(settings); err != nil {
			return false, err
		}
	}
	delete(environment, kimiAuthMarker)
	delete(environment, "ANTHROPIC_AUTH_TOKEN")
	delete(environment, "ANTHROPIC_API_KEY")
	delete(environment, "ANTHROPIC_BASE_URL")
	for name, managed := range kimiEnvironment {
		// Keep changes the user made after activating Kimi.
		if environment[name] != managed {
			continue
		}
		if value, exists := previous[name]; exists {
			environment[name] = value
		} else {
			delete(environment, name)
		}
	}
	if len(environment) == 0 {
		delete(settings, "env")
	}
	return true, nil
}

// A plan discovered only in Claude settings must remain selectable after
// switching away. Never move an unrelated proxy's credential into our store.
func (m *Manager) preservePlanKeys(settings map[string]any) error {
	_, zaiKey := zaiSettings(settings)
	_, kimiKey := kimiSettings(settings)
	for name, key := range map[string]string{"ZAI_API_KEY": zaiKey, "KIMI_API_KEY": kimiKey} {
		if key == "" || readEnvironmentValue(m.paths.Environment, name) != "" {
			continue
		}
		if err := validateAPIKey(key); err != nil {
			return fmt.Errorf("invalid stored plan credential")
		}
		if err := writeEnvironmentValue(m.paths.Environment, name, key); err != nil {
			return fmt.Errorf("preserve plan configuration: %w", err)
		}
		_ = os.Setenv(name, key)
	}
	return nil
}
