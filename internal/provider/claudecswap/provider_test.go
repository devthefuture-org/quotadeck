package claudecswap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/devthefuture-org/quotadeck/internal/domain"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "claudecswap", name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestParseUsageAndScopedWindows(t *testing.T) {
	states, err := Parse(fixture(t, "usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Account.ID != "claude:cswap:2" || states[0].Account.Label != "work" {
		t.Fatalf("unexpected account: %#v", states)
	}
	if got := len(states[0].Snapshot.Windows); got != 4 {
		t.Fatalf("expected every dynamic window, got %d", got)
	}
	if states[0].Snapshot.Windows[2].ID != "scoped-sonnet" {
		t.Fatalf("scoped window not preserved: %#v", states[0].Snapshot.Windows)
	}
}

func TestParseLastGoodIsStale(t *testing.T) {
	states, err := Parse(fixture(t, "last-good.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := states[0].Snapshot
	if snapshot.Status != domain.StatusStale || !snapshot.Stale || len(snapshot.Windows) != 1 {
		t.Fatalf("last good snapshot must be preserved as stale: %#v", snapshot)
	}
	if snapshot.SourceAgeSec == nil || *snapshot.SourceAgeSec != 420 {
		t.Fatalf("unexpected source age: %#v", snapshot.SourceAgeSec)
	}
}

func TestParseDisabledAccount(t *testing.T) {
	states, err := Parse(fixture(t, "disabled.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !states[0].Account.Disabled || states[0].Snapshot.ErrorCode != "account_disabled" {
		t.Fatalf("disabled account not represented: %#v", states[0])
	}
}

func TestParseRejectsUnknownSchema(t *testing.T) {
	if _, err := Parse(fixture(t, "bad-schema.json")); err == nil {
		t.Fatal("expected schema error")
	}
}
