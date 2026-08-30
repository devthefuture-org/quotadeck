package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
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

func TestConcurrentSavesPersistEveryAccount(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "quotadeck.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	const accounts = 24
	errorsChannel := make(chan error, accounts)
	var wait sync.WaitGroup
	for index := range accounts {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			id := fmt.Sprintf("claude:cswap:%d", index)
			account := domain.Account{ID: id, ProviderID: "claude", Label: id, Source: "cswap"}
			snapshot := domain.Snapshot{AccountID: id, FetchedAt: time.Now().UTC(), Status: domain.StatusFresh}
			errorsChannel <- database.Save(t.Context(), account, snapshot)
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	states, err := database.LatestStates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != accounts {
		t.Fatalf("expected %d states, got %d", accounts, len(states))
	}
}

func TestConnectionPragmasApplyToEveryConnection(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "quotadeck.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	connections := make([]*sql.Conn, 0, 4)
	for range 4 {
		connection, err := database.db.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
		defer connection.Close()
	}
	for index, connection := range connections {
		var busyTimeout, foreignKeys int
		if err := connection.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
			t.Fatalf("connection %d busy_timeout: %v", index, err)
		}
		if err := connection.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("connection %d foreign_keys: %v", index, err)
		}
		if busyTimeout != 5000 || foreignKeys != 1 {
			t.Fatalf("connection %d pragmas: busy_timeout=%d foreign_keys=%d", index, busyTimeout, foreignKeys)
		}
	}
}
