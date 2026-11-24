package control

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/devthefuture-org/quotadeck/internal/chatgpt"
)

const (
	chatGPTMarker               = "QUOTADECK_CHATGPT_PLAN"
	chatGPTClassifierPreference = "classifierPreference"
)

var (
	ErrChatGPTGatewayUnavailable = errors.New("start the local QuotaDeck server to use ChatGPT in Claude Code")
	ErrChatGPTClassifierUnknown  = errors.New("choose a classifier model available on your ChatGPT plan")
)

type ChatGPTStatus struct {
	Available            bool   `json:"available"`
	Active               bool   `json:"active"`
	Model                string `json:"model,omitempty"`
	FableModel           string `json:"fableModel,omitempty"`
	ClassifierModel      string `json:"classifierModel,omitempty"`
	ClassifierPreference string `json:"classifierPreference,omitempty"`
	// DefaultClassifier is the configured classifier used without a preference
	// when the plan offers it.
	DefaultClassifier string `json:"defaultClassifier,omitempty"`
}

func (m *Manager) WithChatGPT(endpoint, database string) *Manager {
	m.chatGPTEndpoint = endpoint
	m.chatGPTDatabase = database
	return m
}

// A nil classifier keeps the saved preference. An empty string selects the
// catalog default again.
func (m *Manager) ConfigureChatGPT(catalog []chatgpt.Model, classifier *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chatGPTEndpoint == "" {
		return ErrChatGPTGatewayUnavailable
	}
	if len(catalog) == 0 || strings.TrimSpace(catalog[0].Slug) == "" {
		return errors.New("a ChatGPT model is required")
	}
	preference := ChatGPTClassifierPreference(m.paths.ClaudeSettings)
	if classifier != nil {
		preference = *classifier
	}
	models := chatgpt.SelectClaudeModels(catalog, catalog[0].Slug, preference, m.models.ChatGPT)
	if classifier != nil && preference != "" && preference != models.Classifier {
		return ErrChatGPTClassifierUnknown
	}
	database, err := filepath.Abs(m.chatGPTDatabase)
	if err != nil {
		return err
	}
	if _, err := chatgpt.GatewayKey(database); err != nil {
		return chatgpt.ErrNotConnected
	}
	binary, err := m.runner.LookPath("quotadeck")
	if err != nil {
		return errors.New("quotadeck CLI is required for ChatGPT authentication")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return err
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	helper := quote(binary) + " credential chatgpt --database " + quote(database)

	snapshot, err := snapshotFile(m.paths.ClaudeSettings)
	if err != nil {
		return err
	}
	settings, err := settingsFromSnapshot(snapshot)
	if err != nil {
		return err
	}
	if err := m.preservePlanKeys(settings); err != nil {
		return err
	}
	if _, err := m.disableChatGPT(settings); err != nil {
		return err
	}
	if _, err := m.disableKimi(settings); err != nil {
		return err
	}
	if _, err := m.disableOpenCode(settings); err != nil {
		return err
	}
	if _, err := m.disableZAI(settings); err != nil {
		return err
	}
	_, hadEnvironment := settings["env"]
	env, err := settingsEnvironment(settings)
	if err != nil {
		return err
	}
	managed := models.Environment(m.chatGPTEndpoint)
	managed[chatGPTMarker] = "1"

	previous := map[string]any{}
	for name, value := range managed {
		if old, exists := env[name]; exists {
			previous[name] = old
		}
		env[name] = value
	}
	backup := map[string]any{
		"previousEnvironment": previous, "managedEnvironment": managed,
		"hadEnvironment": hadEnvironment, "managedHelper": helper,
		chatGPTClassifierPreference: preference,
	}
	if err := setChatGPTCompactWindows(settings, backup, models.ContextWindows); err != nil {
		return err
	}
	if old, exists := settings["apiKeyHelper"]; exists {
		backup["previousHelper"] = old
	}
	settings["apiKeyHelper"] = helper
	backupPath := m.chatGPTPreferencesPath()
	previousBackup, err := snapshotFile(backupPath)
	if err != nil {
		return err
	}
	if err := writeSettings(backupPath, backup, 0o600); err != nil {
		return err
	}
	if err := writeSettings(m.paths.ClaudeSettings, settings, snapshot.mode); err != nil {
		return errors.Join(err, restoreFile(backupPath, previousBackup))
	}
	return nil
}

func (m *Manager) DeactivateChatGPT() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot, err := snapshotFile(m.paths.ClaudeSettings)
	if err != nil {
		return err
	}
	settings, err := settingsFromSnapshot(snapshot)
	if err != nil {
		return err
	}
	changed, err := m.disableChatGPT(settings)
	if err != nil || !changed {
		return err
	}
	return writeSettings(m.paths.ClaudeSettings, settings, snapshot.mode)
}

func (m *Manager) chatGPTPreferencesPath() string {
	return chatGPTPreferencesPath(m.paths.ClaudeSettings)
}

func ChatGPTClassifierPreference(claudeSettings string) string {
	preferences, _ := readSettings(chatGPTPreferencesPath(claudeSettings))
	classifier, _ := preferences[chatGPTClassifierPreference].(string)
	return classifier
}

func chatGPTPreferencesPath(claudeSettings string) string {
	return claudeSettings + ".quotadeck-chatgpt.json"
}

func (m *Manager) disableChatGPT(settings map[string]any) (bool, error) {
	env, _ := settings["env"].(map[string]any)
	if env[chatGPTMarker] != "1" {
		return false, nil
	}
	backup, err := readSettings(m.chatGPTPreferencesPath())
	if err != nil {
		return false, fmt.Errorf("read previous Claude Code preferences: %w", err)
	}
	previous, _ := backup["previousEnvironment"].(map[string]any)
	managed, _ := backup["managedEnvironment"].(map[string]any)
	managedHelper, _ := backup["managedHelper"].(string)
	if managed[chatGPTMarker] != "1" || managedHelper == "" {
		return false, errors.New("previous Claude Code preferences for ChatGPT are unavailable")
	}
	for name, value := range managed {
		if current, ok := env[name].(string); !ok || current != value {
			continue
		}
		if old, exists := previous[name]; exists {
			env[name] = old
		} else {
			delete(env, name)
		}
	}
	if settings["apiKeyHelper"] == managedHelper {
		if old, exists := backup["previousHelper"]; exists {
			settings["apiKeyHelper"] = old
		} else {
			delete(settings, "apiKeyHelper")
		}
	}
	if len(env) == 0 && backup["hadEnvironment"] != true {
		delete(settings, "env")
	}
	restoreChatGPTCompactWindows(settings, backup)
	return true, nil
}

const compactWindowSetting = "autoCompactWindow"

func setChatGPTCompactWindows(settings, backup map[string]any, windows map[string]int) error {
	if len(windows) == 0 {
		return nil
	}
	raw, hadModelSettings := settings["modelSettings"]
	byModel, _ := raw.(map[string]any)
	if byModel == nil {
		if hadModelSettings {
			return errors.New("Claude modelSettings must be an object")
		}
		byModel = map[string]any{}
		settings["modelSettings"] = byModel
	}
	previous := map[string]any{}
	for slug, window := range windows {
		row, exists := byModel[slug]
		fields, _ := row.(map[string]any)
		if exists {
			if fields == nil {
				return fmt.Errorf("Claude modelSettings for %s must be an object", slug)
			}
			saved := map[string]any{}
			if old, ok := fields[compactWindowSetting]; ok {
				saved[compactWindowSetting] = old
			}
			previous[slug] = saved
		} else {
			fields = map[string]any{}
			byModel[slug] = fields
		}
		fields[compactWindowSetting] = window
	}
	backup["hadModelSettings"] = hadModelSettings
	backup["previousCompactSettings"] = previous
	backup["managedCompactWindows"] = windows
	return nil
}

func restoreChatGPTCompactWindows(settings, backup map[string]any) {
	byModel, _ := settings["modelSettings"].(map[string]any)
	managed, _ := backup["managedCompactWindows"].(map[string]any)
	previous, _ := backup["previousCompactSettings"].(map[string]any)
	if byModel == nil || len(managed) == 0 {
		return
	}
	for slug, window := range managed {
		fields, _ := byModel[slug].(map[string]any)
		if current, ok := fields[compactWindowSetting].(float64); !ok || current != window {
			continue
		}
		oldFields, existed := previous[slug].(map[string]any)
		if old, ok := oldFields[compactWindowSetting]; ok {
			fields[compactWindowSetting] = old
		} else {
			delete(fields, compactWindowSetting)
			if len(fields) == 0 && !existed {
				delete(byModel, slug)
			}
		}
	}
	if len(byModel) == 0 && backup["hadModelSettings"] != true {
		delete(settings, "modelSettings")
	}
}
