package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateRejectsInvalidProviderRequestTimeouts(t *testing.T) {
	for _, provider := range []string{"zai", "kimi"} {
		path := writeConfig(t, "providers:\n  "+provider+":\n    enabled: true\n    requestTimeout: banana\n")
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), provider+".requestTimeout") {
			t.Fatalf("provider %s: an invalid requestTimeout must be rejected at load, got %v", provider, err)
		}
	}
}

func TestValidateRejectsNonHTTPSKimiBaseURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:9999", "api.kimi.com", "https://"} {
		path := writeConfig(t, "providers:\n  kimi:\n    enabled: true\n    baseURL: "+raw+"\n")
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "baseURL") {
			t.Fatalf("baseURL %q must be rejected at load, got %v", raw, err)
		}
	}
	path := writeConfig(t, "providers:\n  kimi:\n    enabled: true\n    baseURL: https://api.kimi.com/coding/v1\n")
	if _, err := Load(path); err != nil {
		t.Fatalf("a valid https baseURL must load: %v", err)
	}
}

func TestPollingDurationsStillValidate(t *testing.T) {
	path := writeConfig(t, "polling:\n  interval: nope\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "polling.interval") {
		t.Fatalf("an invalid polling interval must be rejected, got %v", err)
	}
}

func TestDefaultTimeouts(t *testing.T) {
	cfg := Default()
	if got := cfg.Providers.ZAI.Timeout(); got != 15*time.Second {
		t.Fatalf("zai timeout default: %v", got)
	}
	if got := cfg.Providers.Kimi.Timeout(); got != 15*time.Second {
		t.Fatalf("kimi timeout default: %v", got)
	}
}
