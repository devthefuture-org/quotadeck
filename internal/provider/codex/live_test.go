package codex

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/domain"
)

// TestLiveCodexAppServer is opt-in because it talks to the locally installed
// Codex CLI and the active CODEX_HOME. It reports only plan/status/window
// counts; account identifiers and credential material never enter test output.
func TestLiveCodexAppServer(t *testing.T) {
	if os.Getenv("QUOTADECK_CODEX_LIVE_TEST") != "1" {
		t.Skip("set QUOTADECK_CODEX_LIVE_TEST=1 to exercise the local Codex app-server")
	}
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		home = config.ExpandPath("~/.codex")
	}
	provider := New("codex", []config.CodexAccountConfig{{Label: "live", Home: home}})
	candidates, err := provider.Discover(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("expected one live CODEX_HOME candidate, got %d", len(candidates))
	}
	accountJSON, limitsJSON, err := provider.rpc(t.Context(), candidates[0].Ref)
	if err != nil {
		t.Fatalf("Codex RPC failed with code %q: %v", domain.ErrorCode(err), err)
	}
	var safeAccount struct {
		Account            json.RawMessage `json:"account"`
		RequiresOpenAIAuth bool            `json:"requiresOpenaiAuth"`
	}
	if err := json.Unmarshal(accountJSON, &safeAccount); err != nil {
		t.Fatalf("decode account envelope: %v", err)
	}
	var safeLimits struct {
		RateLimits          json.RawMessage            `json:"rateLimits"`
		RateLimitsByLimitID map[string]json.RawMessage `json:"rateLimitsByLimitId"`
	}
	if err := json.Unmarshal(limitsJSON, &safeLimits); err != nil {
		t.Fatalf("decode limits envelope: %v", err)
	}
	accountPresent := len(safeAccount.Account) > 0 && string(safeAccount.Account) != "null"
	t.Logf("Codex RPC envelope: accountPresent=%t requiresOpenaiAuth=%t defaultLimits=%t buckets=%d", accountPresent, safeAccount.RequiresOpenAIAuth, len(safeLimits.RateLimits) > 0 && string(safeLimits.RateLimits) != "null", len(safeLimits.RateLimitsByLimitID))

	account, snapshot, err := Parse(accountJSON, limitsJSON, candidates[0])
	if err != nil {
		t.Fatalf("Codex fetch failed with code %q: %v", domain.ErrorCode(err), err)
	}
	t.Logf("Codex live check: plan=%q status=%q windows=%d", account.Plan, snapshot.Status, len(snapshot.Windows))
}
