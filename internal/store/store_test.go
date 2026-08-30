package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/domain"
)

func TestSaveLatestHistoryAndPrune(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "quotadeck.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	account := domain.Account{ID: "claude:cswap:1", ProviderID: "claude", Label: "test", Source: "cswap", SourceMeta: map[string]string{"slot": "1"}}
	used := 38.0
	snapshot := domain.Snapshot{
		AccountID: account.ID, FetchedAt: time.Now().Add(-2 * time.Hour).UTC(), Status: domain.StatusFresh,
		Windows: []domain.QuotaWindow{{ID: "five-hour", Label: "5 hours", UsedPercent: &used}},
	}
	if err := database.Save(ctx, account, snapshot); err != nil {
		t.Fatal(err)
	}
	states, err := database.LatestStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || len(states[0].Snapshot.Windows) != 1 || states[0].Account.SourceMeta["slot"] != "1" {
		t.Fatalf("unexpected current state: %#v", states)
	}
	history, err := database.History(ctx, account.ID, time.Time{}, time.Time{})
	if err != nil || len(history) != 1 {
		t.Fatalf("unexpected history: %#v err=%v", history, err)
	}
	deleted, err := database.Prune(ctx, time.Now().Add(-time.Hour))
	if err != nil || deleted != 1 {
		t.Fatalf("expected one pruned snapshot, deleted=%d err=%v", deleted, err)
	}
}
