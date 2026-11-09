package control

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/provider/opencode"
)

func prepareOpenCode(t *testing.T, m *Manager) {
	t.Helper()
	db, err := sql.Open("sqlite", m.openCode.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE credential(id TEXT PRIMARY KEY,integration_id TEXT,label TEXT,value TEXT,active INTEGER,time_created INTEGER,time_updated INTEGER)`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"type": "oauth", "methodID": "device", "access": "opencode-access-secret", "refresh": "opencode-refresh-secret", "expires": time.Now().Add(time.Hour).UnixMilli(), "metadata": map[string]string{"server": "https://opencode.ai/console", "orgID": "org_fixture"}})
	_, err = db.Exec(`INSERT INTO credential VALUES('cre_fixture','opencode','Personal',?,1,0,0)`, string(raw))
	if err != nil {
		t.Fatal(err)
	}
}
func TestOpenCodeRoundTripRestoresModelsAndHelper(t *testing.T) {
	for _, next := range []string{"claude", "zai", "kimi"} {
		t.Run(next, func(t *testing.T) {
			t.Setenv("KIMI_API_KEY", "")
			t.Setenv("KIMI_CODING_API_KEY", "")
			m, paths := testManager(t, &fakeRunner{})
			prepareOpenCode(t, m)
			original := map[string]any{"apiKeyHelper": "original-helper", "env": map[string]any{"ANTHROPIC_MODEL": "original-model", "ANTHROPIC_CUSTOM_HEADERS": "x-example: retained", "OTHER": "keep", claudeAiConnectorsSwitch: "true"}}
			if err := writeSettings(paths.ClaudeSettings, original, 0o600); err != nil {
				t.Fatal(err)
			}
			// Enter from Kimi, then go back through Kimi too: its saved preferences must
			// never accidentally capture another provider's model or credential helper.
			if err := m.ConfigureKimi(t.Context(), "kimi-fixture", true); err != nil {
				t.Fatal(err)
			}
			if err := m.ConfigureOpenCode(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := m.ConfigureOpenCode(t.Context()); err != nil {
				t.Fatal(err)
			}
			settings, _ := readSettings(paths.ClaudeSettings)
			env, _ := settingsEnvironment(settings)
			if env["ANTHROPIC_MODEL"] != opencode.Model || env["ANTHROPIC_AUTH_TOKEN"] != "" || !openCodeActive(settings) {
				t.Fatal("OpenCode not fully configured")
			}
			if env[claudeAiConnectorsSwitch] != claudeAiConnectorsOff {
				t.Fatalf("claude.ai connectors must stay off behind the OpenCode gateway: %#v", env)
			}
			raw, _ := json.Marshal([]any{settings, m.Status(nil)})
			if strings.Contains(string(raw), "opencode-access-secret") || strings.Contains(string(raw), "opencode-refresh-secret") {
				t.Fatal("stored credential leaked into Claude settings or status")
			}
			if next == "kimi" {
				if err := m.ConfigureKimi(t.Context(), "", true); err != nil {
					t.Fatal(err)
				}
			}
			if next == "zai" {
				if err := m.ConfigureZAI(t.Context(), "zai-fixture", true); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.SwitchClaude(t.Context(), "claude:cswap:1"); err != nil {
				t.Fatal(err)
			}
			settings, _ = readSettings(paths.ClaudeSettings)
			if !reflect.DeepEqual(settings, original) {
				t.Fatalf("preferences not restored: %+v", settings)
			}
			info, _ := os.Stat(paths.ClaudeSettings)
			if info.Mode().Perm() != 0o600 {
				t.Fatal("settings are not private")
			}
		})
	}
}
func TestOpenCodeMissingLoginLeavesSettingsUntouched(t *testing.T) {
	m, paths := testManager(t, &fakeRunner{})
	os.MkdirAll(filepath.Dir(paths.ClaudeSettings), 0o700)
	before := []byte(`{"env":{"ANTHROPIC_MODEL":"keep"}}`)
	os.WriteFile(paths.ClaudeSettings, before, 0o600)
	if err := m.ConfigureOpenCode(t.Context()); err == nil {
		t.Fatal("missing login activated")
	}
	after, _ := os.ReadFile(paths.ClaudeSettings)
	if string(before) != string(after) {
		t.Fatal("failed activation changed settings")
	}
}
