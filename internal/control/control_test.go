package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/runner"
)

type fakeRunner struct {
	args       []string
	err        error
	accounts   int
	paths      map[string]bool
	installRun bool
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	if f.paths != nil && !f.paths[name] {
		return "", os.ErrNotExist
	}
	return "/bin/" + name, nil
}
func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (runner.Result, error) {
	f.args = append([]string{name}, args...)
	if f.err != nil {
		return runner.Result{}, f.err
	}
	if name == "uv" {
		f.installRun = true
		if f.paths != nil {
			f.paths["cswap"] = true
		}
	}
	if name == "cswap" && len(args) > 0 && args[0] == "add" {
		f.accounts++
	}
	if name == "cswap" && len(args) > 1 && args[0] == "list" && args[1] == "--json" {
		accounts := make([]map[string]any, f.accounts)
		return runner.Result{Stdout: mustJSON(accounts)}, nil
	}
	return runner.Result{Stdout: []byte(`{"schemaVersion":1}`)}, nil
}

func mustJSON(accounts []map[string]any) []byte {
	payload, _ := json.Marshal(map[string]any{"schemaVersion": 1, "accounts": accounts})
	return payload
}

func testManager(t *testing.T, commandRunner runner.Runner) (*Manager, Paths) {
	t.Helper()
	root := t.TempDir()
	paths := Paths{
		Environment:    filepath.Join(root, "config", "environment"),
		ClaudeSettings: filepath.Join(root, "claude", "settings.json"),
	}
	return New("cswap", commandRunner, paths), paths
}

func TestConfigureZAIStoresPrivateKeyAndNeverReturnsIt(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "")
	manager, paths := testManager(t, &fakeRunner{})
	secret := "zai-secret-fixture"
	if err := manager.ConfigureZAI(t.Context(), secret, true); err != nil {
		t.Fatal(err)
	}

	environment, err := os.ReadFile(paths.Environment)
	if err != nil {
		t.Fatal(err)
	}
	if string(environment) != "ZAI_API_KEY="+secret+"\n" {
		t.Fatalf("unexpected environment file")
	}
	info, err := os.Stat(paths.Environment)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("environment mode = %o", info.Mode().Perm())
	}

	settings, err := readSettings(paths.ClaudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	active, key := zaiSettings(settings)
	if !active || key != secret {
		t.Fatalf("Z.ai settings were not activated")
	}
	status := manager.Status(nil)
	body, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatal("control status leaked the Z.ai key")
	}
	if !status.ZAI.Configured || !status.ZAI.Active || status.Mode != "zai" {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestSwitchClaudeDisablesZAIAndRunsExactSlot(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "")
	commandRunner := &fakeRunner{}
	manager, paths := testManager(t, commandRunner)
	settings := map[string]any{"theme": "dark", "env": map[string]any{
		"ANTHROPIC_AUTH_TOKEN": "existing-zai-key",
		"ANTHROPIC_BASE_URL":   zaiEndpoint,
		"API_TIMEOUT_MS":       managedTimeout,
		"KEEP_ME":              "yes",
	}}
	if err := writeSettings(paths.ClaudeSettings, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.SwitchClaude(t.Context(), "claude:cswap:3"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commandRunner.args, []string{"cswap", "switch", "3", "--json"}) {
		t.Fatalf("unexpected command: %#v", commandRunner.args)
	}
	updated, err := readSettings(paths.ClaudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	environment := updated["env"].(map[string]any)
	if environment["KEEP_ME"] != "yes" || environment["ANTHROPIC_AUTH_TOKEN"] != nil || environment["ANTHROPIC_BASE_URL"] != nil {
		t.Fatalf("unexpected settings: %#v", updated)
	}
	if key := readEnvironmentValue(paths.Environment, "ZAI_API_KEY"); key != "existing-zai-key" {
		t.Fatalf("Z.ai key was not preserved")
	}
	status := manager.Status([]domain.AccountState{{Account: domain.Account{ID: "claude:cswap:3", ProviderID: "claude", Active: true}}})
	if status.Mode != "claude" || status.Claude.ActiveAccountID != "claude:cswap:3" || !status.ZAI.Configured || status.ZAI.Active {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestSwitchClaudeRestoresSettingsWhenCswapFails(t *testing.T) {
	commandRunner := &fakeRunner{err: errors.New("switch failed")}
	manager, paths := testManager(t, commandRunner)
	original := []byte(`{"env":{"ANTHROPIC_AUTH_TOKEN":"secret","ANTHROPIC_BASE_URL":"https://api.z.ai/api/anthropic"},"keep":true}`)
	if err := os.MkdirAll(filepath.Dir(paths.ClaudeSettings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.ClaudeSettings, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.SwitchClaude(t.Context(), "claude:cswap:1"); err == nil {
		t.Fatal("expected switch failure")
	}
	restored, err := os.ReadFile(paths.ClaudeSettings)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("settings were not restored: %s", restored)
	}
}

func TestConfigureZAIRejectsMultilineKey(t *testing.T) {
	manager, _ := testManager(t, &fakeRunner{})
	if err := manager.ConfigureZAI(t.Context(), "valid-prefix\nINJECTED=value", true); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected invalid key, got %v", err)
	}
}

func TestStatusDoesNotTreatUnrelatedAnthropicProxyAsZAI(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "")
	manager, paths := testManager(t, &fakeRunner{})
	settings := map[string]any{"env": map[string]any{
		"ANTHROPIC_AUTH_TOKEN": "unrelated-proxy-token",
		"ANTHROPIC_BASE_URL":   "https://proxy.example.com/anthropic",
	}}
	if err := writeSettings(paths.ClaudeSettings, settings, 0o600); err != nil {
		t.Fatal(err)
	}
	status := manager.Status(nil)
	if status.ZAI.Configured || status.ZAI.Active || status.Mode == "zai" {
		t.Fatalf("unrelated Anthropic proxy was classified as Z.ai: %#v", status)
	}
}

func TestSwitchClaudeRejectsArgumentInjection(t *testing.T) {
	manager, _ := testManager(t, &fakeRunner{})
	if err := manager.SwitchClaude(t.Context(), "claude:cswap:1 --force"); !errors.Is(err, ErrInvalidAccount) {
		t.Fatalf("expected invalid account, got %v", err)
	}
}

func TestSetupClaudeInstallsWithUVAndAddsCurrentLogin(t *testing.T) {
	commandRunner := &fakeRunner{paths: map[string]bool{"uv": true}}
	manager, _ := testManager(t, commandRunner)

	result, err := manager.SetupClaude(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || !result.AccountAdded || result.AccountCount != 1 || !commandRunner.installRun {
		t.Fatalf("unexpected setup result: %#v", result)
	}
}

func TestSetupClaudeLeavesExistingAccountsUntouched(t *testing.T) {
	commandRunner := &fakeRunner{accounts: 2}
	manager, _ := testManager(t, commandRunner)

	result, err := manager.SetupClaude(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Installed || result.AccountAdded || result.AccountCount != 2 {
		t.Fatalf("unexpected setup result: %#v", result)
	}
	if !reflect.DeepEqual(commandRunner.args, []string{"cswap", "list", "--json"}) {
		t.Fatalf("setup mutated existing accounts: %#v", commandRunner.args)
	}
}

func TestSetupClaudeRequiresSupportedInstaller(t *testing.T) {
	commandRunner := &fakeRunner{paths: map[string]bool{}}
	manager, _ := testManager(t, commandRunner)

	if _, err := manager.SetupClaude(t.Context()); !errors.Is(err, ErrCswapInstallerMissing) {
		t.Fatalf("expected missing installer error, got %v", err)
	}
}
