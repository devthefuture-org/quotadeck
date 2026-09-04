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
	"github.com/devthefuture-org/quotadeck/internal/runner"
)

const (
	zaiEndpoint    = "https://api.z.ai/api/anthropic"
	managedTimeout = "3000000"
)

var (
	ErrInvalidAccount        = errors.New("invalid Claude account")
	ErrInvalidKey            = errors.New("invalid Z.ai API key")
	ErrZAINotConfigured      = errors.New("Z.ai is not configured")
	ErrCswapInstallerMissing = errors.New("neither uv nor pipx is available to install cswap")
	ErrCswapCustomBinary     = errors.New("automatic installation requires the default cswap binary name")
	ErrClaudeLoginRequired   = errors.New("Claude Code must be logged in before cswap can add an account")
)

type Paths struct {
	Environment    string
	ClaudeSettings string
}

type Status struct {
	Mode   string       `json:"mode"`
	Claude ClaudeStatus `json:"claude"`
	ZAI    ZAIStatus    `json:"zai"`
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

type Manager struct {
	claudeBinary string
	runner       runner.Runner
	paths        Paths

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

func (m *Manager) Status(states []domain.AccountState) Status {
	status := Status{Mode: "unknown", ZAI: ZAIStatus{Endpoint: zaiEndpoint}}
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
	activeZAI, settingsKey := zaiSettings(settings)
	if activeZAI && readEnvironmentValue(m.paths.Environment, "ZAI_API_KEY") == "" && settingsKey != "" {
		if err := writeEnvironmentValue(m.paths.Environment, "ZAI_API_KEY", settingsKey); err != nil {
			return fmt.Errorf("preserve Z.ai configuration: %w", err)
		}
		_ = os.Setenv("ZAI_API_KEY", settingsKey)
	}
	changed := disableZAI(settings)
	if changed {
		if err := writeSettings(m.paths.ClaudeSettings, settings, snapshot.mode); err != nil {
			return fmt.Errorf("disable Z.ai for Claude Code: %w", err)
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

	effectiveKey := firstNonEmpty(apiKey, readEnvironmentValue(m.paths.Environment, "ZAI_API_KEY"), os.Getenv("ZAI_API_KEY"))
	if effectiveKey == "" {
		return ErrZAINotConfigured
	}
	if !activate {
		return nil
	}
	settings, err := readSettings(m.paths.ClaudeSettings)
	if err != nil {
		return fmt.Errorf("read Claude settings: %w", err)
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

func enableZAI(settings map[string]any, apiKey string) error {
	environment, err := settingsEnvironment(settings)
	if err != nil {
		return err
	}
	environment["ANTHROPIC_AUTH_TOKEN"] = apiKey
	environment["ANTHROPIC_BASE_URL"] = zaiEndpoint
	environment["API_TIMEOUT_MS"] = managedTimeout
	return nil
}

func disableZAI(settings map[string]any) bool {
	environment, ok := settings["env"].(map[string]any)
	if !ok {
		return false
	}
	baseURL, _ := environment["ANTHROPIC_BASE_URL"].(string)
	if !recognizedZAI(baseURL) {
		return false
	}
	delete(environment, "ANTHROPIC_AUTH_TOKEN")
	delete(environment, "ANTHROPIC_BASE_URL")
	if timeout, _ := environment["API_TIMEOUT_MS"].(string); timeout == managedTimeout {
		delete(environment, "API_TIMEOUT_MS")
	}
	if len(environment) == 0 {
		delete(settings, "env")
	}
	return true
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
