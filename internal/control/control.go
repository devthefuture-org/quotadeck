package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/provider/opencode"
	"github.com/devthefuture-org/quotadeck/internal/runner"
)

const (
	zaiEndpoint    = "https://api.z.ai/api/anthropic"
	managedTimeout = "3000000"
)

// Claude Code's auto mode asks the API to review each action server-side.
// Anthropic-compatible gateways answer those requests without a verdict, and
// Claude Code retired its local fallback classifier in 2.1.31x, so auto mode
// denies every action it would check behind a gateway. "0" keeps Claude
// Code's own classifier requests, which gateways serve like any other call.
const autoModeServerSwitch = "CLAUDE_CODE_AUTO_MODE_SERVER"

// claude.ai connectors auto-load remote MCP servers whose responses carry
// Anthropic-native mcp_tool_use blocks. Translated gateway targets reject
// those blocks (opencode-go answers "requires a native Anthropic Messages
// target"), so behind a gateway the session history must stay plain
// tool_use: "false" stops the automatic connector loading.
const claudeAiConnectorsSwitch = "ENABLE_CLAUDEAI_MCP_SERVERS"
const claudeAiConnectorsOff = "false"

var (
	ErrInvalidAccount        = errors.New("invalid Claude account")
	ErrInvalidKey            = errors.New("invalid Z.ai API key")
	ErrZAINotConfigured      = errors.New("Z.ai is not configured")
	ErrInvalidKimiKey        = errors.New("invalid Kimi API key")
	ErrKimiNotConfigured     = errors.New("Kimi is not configured")
	ErrCswapInstallerMissing = errors.New("neither uv nor pipx is available to install cswap")
	ErrCswapCustomBinary     = errors.New("automatic installation requires the default cswap binary name")
	ErrClaudeLoginRequired   = errors.New("Claude Code must be logged in before cswap can add an account")
)

type Paths struct {
	Environment    string
	ClaudeSettings string
}

type Status struct {
	OpenCode KimiStatus   `json:"opencode"`
	Mode     string       `json:"mode"`
	Claude   ClaudeStatus `json:"claude"`
	ZAI      ZAIStatus    `json:"zai"`
	Kimi     KimiStatus   `json:"kimi"`
}

type ClaudeStatus struct {
	Available       bool   `json:"available"`
	ActiveAccountID string `json:"activeAccountId,omitempty"`
}

type ClaudeSetupResult struct {
	Installed    bool `json:"installed"`
	AccountAdded bool `json:"accountAdded"`
	AccountCount int  `json:"accountCount"`
}

type ZAIStatus struct {
	Configured bool   `json:"configured"`
	Active     bool   `json:"active"`
	Endpoint   string `json:"endpoint"`
}

type KimiStatus struct {
	ZAIStatus
	Model          string `json:"model,omitempty"`
	Source         string `json:"source,omitempty"`
	LocalAvailable bool   `json:"localAvailable"`
}

type Manager struct {
	claudeBinary string
	runner       runner.Runner
	paths        Paths
	kimiHomes    []string
	openCode     config.OpenCodeConfig

	mu sync.Mutex
}

func DefaultPaths(settingsPaths []string) Paths {
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		configDir = "."
	}
	settingsPath := "~/.claude/settings.json"
	if len(settingsPaths) > 0 && strings.TrimSpace(settingsPaths[0]) != "" {
		settingsPath = settingsPaths[0]
	}
	return Paths{
		Environment:    filepath.Join(configDir, "quotadeck", "environment"),
		ClaudeSettings: config.ExpandPath(settingsPath),
	}
}

func New(claudeBinary string, commandRunner runner.Runner, paths Paths) *Manager {
	if strings.TrimSpace(claudeBinary) == "" {
		claudeBinary = "cswap"
	}
	if commandRunner == nil {
		commandRunner = runner.ExecRunner{}
	}
	return &Manager{claudeBinary: claudeBinary, runner: commandRunner, paths: paths}
}

func (m *Manager) WithKimiHomes(homes []string) *Manager {
	m.kimiHomes = append([]string(nil), homes...)
	return m
}

func (m *Manager) Status(states []domain.AccountState) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := Status{Mode: "unknown", ZAI: ZAIStatus{Endpoint: zaiEndpoint}, Kimi: KimiStatus{ZAIStatus: ZAIStatus{Endpoint: kimiEndpoint}}}
	if _, err := m.runner.LookPath(m.claudeBinary); err == nil {
		status.Claude.Available = true
	}
	for _, state := range states {
		if state.Account.ProviderID == "claude" && state.Account.Active {
			status.Claude.ActiveAccountID = state.Account.ID
			status.Mode = "claude"
			break
		}
	}
	settings, err := readSettings(m.paths.ClaudeSettings)
	if err != nil {
		settings = map[string]any{}
	}
	active, settingsKey := zaiSettings(settings)
	status.ZAI.Active = active
	status.ZAI.Configured = firstNonEmpty(
		readEnvironmentValue(m.paths.Environment, "ZAI_API_KEY"),
		os.Getenv("ZAI_API_KEY"),
		settingsKey,
	) != ""
	if active {
		status.Mode = "zai"
	}
	status.Kimi.Active, settingsKey = kimiSettings(settings)
	status.Kimi.Configured = m.kimiKey(settingsKey) != ""
	if status.Kimi.Configured {
		status.Kimi.Source = "api-key"
	}
	if credential, ok := m.localKimi(); ok {
		status.Kimi.LocalAvailable = true
		if !status.Kimi.Configured {
			status.Kimi.Configured = true
			status.Kimi.Source = "kimi-cli"
			status.Kimi.Endpoint = credential.Endpoint()
		}
	}
	if kimiHelperActive(settings) {
		status.Kimi.Active = true
		status.Kimi.Source = "kimi-cli"
	}
	if status.Kimi.Active {
		status.Mode = "kimi"
		environment, _ := settings["env"].(map[string]any)
		status.Kimi.Model, _ = environment["ANTHROPIC_MODEL"].(string)
		status.Kimi.Endpoint, _ = environment["ANTHROPIC_BASE_URL"].(string)
	}
	status.OpenCode.Endpoint = opencode.Endpoint
	if _, ok := m.localOpenCode(); ok {
		status.OpenCode.Configured = true
		status.OpenCode.LocalAvailable = true
		status.OpenCode.Source = "opencode-v2"
	}
	if openCodeActive(settings) {
		status.OpenCode.Active = true
		status.Mode = "opencode"
		env, _ := settings["env"].(map[string]any)
		status.OpenCode.Model, _ = env["ANTHROPIC_MODEL"].(string)
	}
	return status
}

func (m *Manager) SwitchClaude(ctx context.Context, accountID string) error {
	slot, err := claudeSlot(accountID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.runner.LookPath(m.claudeBinary); err != nil {
		return fmt.Errorf("cswap is unavailable")
	}

	snapshot, err := snapshotFile(m.paths.ClaudeSettings)
	if err != nil {
		return fmt.Errorf("read Claude settings: %w", err)
	}
	settings, err := settingsFromSnapshot(snapshot)
	if err != nil {
		return fmt.Errorf("parse Claude settings: %w", err)
	}
	if err := m.preservePlanKeys(settings); err != nil {
		return err
	}
	changed, err := m.disableKimi(settings)
	if err != nil {
		return err
	}
	otherChanged, err := m.disableOpenCode(settings)
	if err != nil {
		return err
	}
	zaiChanged, err := m.disableZAI(settings)
	if err != nil {
		return err
	}
	changed = otherChanged || zaiChanged || changed
	if changed {
		if err := writeSettings(m.paths.ClaudeSettings, settings, snapshot.mode); err != nil {
			return fmt.Errorf("disable API plan for Claude Code: %w", err)
		}
	}
	if _, err := m.runner.Run(ctx, m.claudeBinary, "switch", strconv.Itoa(slot), "--json"); err != nil {
		if changed {
			_ = restoreFile(m.paths.ClaudeSettings, snapshot)
		}
		return fmt.Errorf("cswap could not switch account: %w", err)
	}
	return nil
}

// SetupClaude installs the supported claude-swap package when necessary and
// imports the current Claude Code login when no cswap account exists yet.
// Credentials remain entirely inside cswap; QuotaDeck only invokes its public
// commands and inspects the documented, non-secret JSON account list.
func (m *Manager) SetupClaude(ctx context.Context) (ClaudeSetupResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	result := ClaudeSetupResult{}
	if _, err := m.runner.LookPath(m.claudeBinary); err != nil {
		if filepath.Base(m.claudeBinary) != m.claudeBinary || m.claudeBinary != "cswap" {
			return result, ErrCswapCustomBinary
		}
		installer, args, err := m.csSwapInstaller()
		if err != nil {
			return result, err
		}
		if _, err := m.runner.Run(ctx, installer, args...); err != nil {
			return result, fmt.Errorf("install claude-swap: %w", err)
		}
		if _, err := m.runner.LookPath(m.claudeBinary); err != nil {
			return result, fmt.Errorf("cswap was installed but is not executable: %w", err)
		}
		result.Installed = true
	}

	count, err := m.csSwapAccountCount(ctx)
	if err != nil {
		return result, err
	}
	if count > 0 {
		result.AccountCount = count
		return result, nil
	}
	if _, err := m.runner.Run(ctx, m.claudeBinary, "add"); err != nil {
		return result, fmt.Errorf("%w: %v", ErrClaudeLoginRequired, err)
	}
	count, err = m.csSwapAccountCount(ctx)
	if err != nil {
		return result, err
	}
	if count == 0 {
		return result, ErrClaudeLoginRequired
	}
	result.AccountAdded = true
	result.AccountCount = count
	return result, nil
}

func (m *Manager) csSwapInstaller() (string, []string, error) {
	if _, err := m.runner.LookPath("uv"); err == nil {
		return "uv", []string{"tool", "install", "claude-swap"}, nil
	}
	if _, err := m.runner.LookPath("pipx"); err == nil {
		return "pipx", []string{"install", "claude-swap"}, nil
	}
	return "", nil, ErrCswapInstallerMissing
}

func (m *Manager) csSwapAccountCount(ctx context.Context) (int, error) {
	output, err := m.runner.Run(ctx, m.claudeBinary, "list", "--json")
	if err != nil {
		return 0, fmt.Errorf("inspect cswap accounts: %w", err)
	}
	var payload struct {
		SchemaVersion int               `json:"schemaVersion"`
		Accounts      []json.RawMessage `json:"accounts"`
		Error         json.RawMessage   `json:"error"`
	}
	if err := json.Unmarshal(output.Stdout, &payload); err != nil {
		return 0, errors.New("cswap returned invalid JSON while checking setup")
	}
	if payload.SchemaVersion != 1 || len(payload.Error) > 0 && string(payload.Error) != "null" {
		return 0, errors.New("cswap could not report its account setup")
	}
	return len(payload.Accounts), nil
}

func (m *Manager) ConfigureZAI(_ context.Context, apiKey string, activate bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		if err := validateAPIKey(apiKey); err != nil {
			return err
		}
		if err := writeEnvironmentValue(m.paths.Environment, "ZAI_API_KEY", apiKey); err != nil {
			return fmt.Errorf("store Z.ai configuration: %w", err)
		}
		if err := os.Setenv("ZAI_API_KEY", apiKey); err != nil {
			return fmt.Errorf("activate Z.ai configuration: %w", err)
		}
	}

	settings, err := readSettings(m.paths.ClaudeSettings)
	if err != nil {
		return fmt.Errorf("read Claude settings: %w", err)
	}
	wasActive, settingsKey := zaiSettings(settings)
	effectiveKey := firstNonEmpty(apiKey, readEnvironmentValue(m.paths.Environment, "ZAI_API_KEY"), os.Getenv("ZAI_API_KEY"), settingsKey)
	if effectiveKey == "" {
		return ErrZAINotConfigured
	}
	if !activate {
		return nil
	}
	if err := m.preservePlanKeys(settings); err != nil {
		return err
	}
	if _, err := m.disableKimi(settings); err != nil {
		return err
	}
	if _, err := m.disableOpenCode(settings); err != nil {
		return err
	}
	if !wasActive {
		if err := m.preserveZAIKeys(settings); err != nil {
			return err
		}
	}
	if err := enableZAI(settings, effectiveKey); err != nil {
		return fmt.Errorf("update Claude settings: %w", err)
	}
	if err := writeSettings(m.paths.ClaudeSettings, settings, 0o600); err != nil {
		return fmt.Errorf("configure Claude Code for Z.ai: %w", err)
	}
	return nil
}

func claudeSlot(accountID string) (int, error) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(accountID), "claude:cswap:")
	if !ok {
		return 0, ErrInvalidAccount
	}
	slot, err := strconv.Atoi(raw)
	if err != nil || slot <= 0 {
		return 0, ErrInvalidAccount
	}
	return slot, nil
}

func validateAPIKey(value string) error {
	if len(value) < 8 || len(value) > 4096 {
		return ErrInvalidKey
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return ErrInvalidKey
		}
	}
	return nil
}

func readSettings(path string) (map[string]any, error) {
	snapshot, err := snapshotFile(path)
	if err != nil {
		return nil, err
	}
	return settingsFromSnapshot(snapshot)
}

func settingsFromSnapshot(snapshot fileState) (map[string]any, error) {
	if !snapshot.exists || len(strings.TrimSpace(string(snapshot.body))) == 0 {
		return map[string]any{}, nil
	}
	var settings map[string]any
	if err := json.Unmarshal(snapshot.body, &settings); err != nil {
		return nil, err
	}
	if settings == nil {
		settings = map[string]any{}
	}
	return settings, nil
}

func settingsEnvironment(settings map[string]any) (map[string]any, error) {
	raw, ok := settings["env"]
	if !ok || raw == nil {
		environment := map[string]any{}
		settings["env"] = environment
		return environment, nil
	}
	environment, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("Claude settings env must be an object")
	}
	return environment, nil
}

func zaiSettings(settings map[string]any) (bool, string) {
	raw, ok := settings["env"].(map[string]any)
	if !ok {
		return false, ""
	}
	baseURL, _ := raw["ANTHROPIC_BASE_URL"].(string)
	apiKey, _ := raw["ANTHROPIC_AUTH_TOKEN"].(string)
	apiKey = strings.TrimSpace(apiKey)
	if !recognizedZAI(baseURL) {
		return false, ""
	}
	return apiKey != "", apiKey
}

func recognizedZAI(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	return host == "api.z.ai" || host == "z.ai" || strings.HasSuffix(host, ".z.ai") || host == "open.bigmodel.cn" || strings.HasSuffix(host, ".bigmodel.cn")
}

// zaiManagedEnvironment lists the fixed-value env keys enableZAI writes.
// disableZAI restores the user's previous value for each key the user has
// not changed after activating Z.ai.
var zaiManagedEnvironment = map[string]string{
	"API_TIMEOUT_MS":         managedTimeout,
	autoModeServerSwitch:     "0",
	claudeAiConnectorsSwitch: claudeAiConnectorsOff,
}

func enableZAI(settings map[string]any, apiKey string) error {
	environment, err := settingsEnvironment(settings)
	if err != nil {
		return err
	}
	environment["ANTHROPIC_AUTH_TOKEN"] = apiKey
	delete(environment, "ANTHROPIC_API_KEY")
	environment["ANTHROPIC_BASE_URL"] = zaiEndpoint
	for name, value := range zaiManagedEnvironment {
		environment[name] = value
	}
	return nil
}

func (m *Manager) preserveZAIKeys(settings map[string]any) error {
	if _, err := os.Stat(m.zaiPreferencesPath()); err == nil {
		// The sidecar holds the preferences from before the FIRST activation:
		// a later activation sees env values that may already be managed ones.
		return nil
	}
	environment, err := settingsEnvironment(settings)
	if err != nil {
		return err
	}
	previous := map[string]any{}
	for name := range zaiManagedEnvironment {
		if value, exists := environment[name]; exists {
			previous[name] = value
		}
	}
	if err := writeSettings(m.zaiPreferencesPath(), previous, 0o600); err != nil {
		return fmt.Errorf("preserve Claude environment preferences: %w", err)
	}
	return nil
}

func (m *Manager) zaiPreferencesPath() string {
	return m.paths.ClaudeSettings + ".quotadeck-zai.json"
}

func (m *Manager) disableZAI(settings map[string]any) (bool, error) {
	environment, ok := settings["env"].(map[string]any)
	if !ok {
		return false, nil
	}
	baseURL, _ := environment["ANTHROPIC_BASE_URL"].(string)
	if !recognizedZAI(baseURL) {
		return false, nil
	}
	previous, err := readSettings(m.zaiPreferencesPath())
	if err != nil {
		return false, fmt.Errorf("read Claude environment preferences: %w", err)
	}
	delete(environment, "ANTHROPIC_AUTH_TOKEN")
	delete(environment, "ANTHROPIC_BASE_URL")
	for name, managed := range zaiManagedEnvironment {
		// Keep changes the user made after activating Z.ai.
		if value, _ := environment[name].(string); value != managed {
			continue
		}
		if old, exists := previous[name]; exists {
			environment[name] = old
		} else {
			delete(environment, name)
		}
	}
	if len(environment) == 0 {
		delete(settings, "env")
	}
	return true, nil
}

type fileState struct {
	exists bool
	body   []byte
	mode   fs.FileMode
}

func snapshotFile(path string) (fileState, error) {
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileState{mode: 0o600}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fileState{}, err
	}
	return fileState{exists: true, body: body, mode: info.Mode().Perm()}, nil
}

func restoreFile(path string, snapshot fileState) error {
	if !snapshot.exists {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return atomicWrite(path, snapshot.body, snapshot.mode)
}

func writeSettings(path string, settings map[string]any, mode fs.FileMode) error {
	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if mode == 0 {
		mode = 0o600
	}
	return atomicWrite(path, body, mode)
}

func atomicWrite(path string, body []byte, mode fs.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".quotadeck-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func readEnvironmentValue(path, name string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == name {
			value = strings.TrimSpace(value)
			if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
				value = value[1 : len(value)-1]
			}
			return value
		}
	}
	return ""
}

func writeEnvironmentValue(path, name, value string) error {
	var lines []string
	if body, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	replacement := name + "=" + value
	found := false
	for index, line := range lines {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		key, _, ok := strings.Cut(trimmed, "=")
		if ok && strings.TrimSpace(key) == name {
			if !found {
				lines[index] = replacement
				found = true
			} else {
				lines[index] = ""
			}
		}
	}
	if !found {
		lines = append(lines, replacement)
	}
	filtered := lines[:0]
	for _, line := range lines {
		if line != "" {
			filtered = append(filtered, line)
		}
	}
	body := []byte(strings.Join(filtered, "\n") + "\n")
	return atomicWrite(path, body, 0o600)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
