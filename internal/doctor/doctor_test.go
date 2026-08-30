package doctor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

func TestReportContainsPresenceButNeverSecretValue(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "doctor-must-not-leak-this")
	cfg := config.Default()
	cfg.Providers.ZAI.Accounts = nil
	report := (Collector{Config: cfg, ConfigPath: "/tmp/config.yaml", Version: "test"}).Collect(t.Context())
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "doctor-must-not-leak-this") {
		t.Fatal("doctor leaked a secret value")
	}
	if !strings.Contains(text, `"secretPresent":"true"`) {
		t.Fatalf("doctor should report presence metadata: %s", text)
	}
}

func TestDisabledProvidersNeverAcceptSources(t *testing.T) {
	t.Setenv("ZAI_API_KEY", "present-but-disabled")
	cfg := config.Default()
	cfg.Providers.Claude.Enabled = false
	cfg.Providers.ZAI.Enabled = false
	cfg.Providers.Codex.Enabled = false

	report := (Collector{Config: cfg, Version: "test"}).Collect(t.Context())
	for _, source := range report.Sources {
		if source.Accepted {
			t.Fatalf("disabled provider %q accepted source %q", source.Provider, source.Source)
		}
		if source.Reason != "provider disabled" {
			t.Fatalf("disabled provider %q reported reason %q", source.Provider, source.Reason)
		}
	}
}
