package poller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/store"
)

type failingProvider struct {
	candidate domain.AccountCandidate
	err       error
}

type emptyProvider struct{ id string }

func (p emptyProvider) ID() string   { return p.id }
func (p emptyProvider) Name() string { return "Empty provider" }
func (p emptyProvider) Discover(context.Context) ([]domain.AccountCandidate, error) {
	return nil, nil
}
func (p emptyProvider) Fetch(context.Context, domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	return domain.Account{}, domain.Snapshot{}, errors.New("unexpected fetch")
}

func (p failingProvider) ID() string   { return p.candidate.ProviderID }
func (p failingProvider) Name() string { return "Failing provider" }
func (p failingProvider) Discover(context.Context) ([]domain.AccountCandidate, error) {
	return []domain.AccountCandidate{p.candidate}, nil
}

func TestRefreshMarksPreviousStateUnavailableWhenSourceDisappears(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/quotadeck.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	used := 37.0
	account := domain.Account{ID: "zai:personal", ProviderID: "zai", Label: "Personal", Source: "env"}
	previous := domain.Snapshot{
		AccountID: account.ID, FetchedAt: time.Now().Add(-time.Minute), Status: domain.StatusFresh,
		Windows: []domain.QuotaWindow{{ID: "tokens", Label: "Tokens", UsedPercent: &used}},
	}
	if err := database.Save(t.Context(), account, previous); err != nil {
		t.Fatal(err)
	}

	engine := New(database, []domain.Provider{emptyProvider{id: "zai"}}, time.Minute, time.Second, 30)
	if err := engine.Refresh(t.Context()); err == nil {
		t.Fatal("expected refresh to report the missing source")
	}

	state, err := database.Latest(t.Context(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Snapshot.Status != domain.StatusUnavailable || !state.Snapshot.Stale {
		t.Fatalf("expected stale unavailable state, got %#v", state.Snapshot)
	}
	if state.Snapshot.ErrorCode != "source_missing" {
		t.Fatalf("expected source_missing, got %q", state.Snapshot.ErrorCode)
	}
	if len(state.Snapshot.Windows) != 1 {
		t.Fatalf("expected previous quota windows to remain visible, got %#v", state.Snapshot.Windows)
	}
}
func (p failingProvider) Fetch(context.Context, domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	return domain.Account{}, domain.Snapshot{}, p.err
}

func TestRefreshReportsAccountFailureAndPreservesAuthStatus(t *testing.T) {
	database, err := store.Open(t.TempDir() + "/quotadeck.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	used := 42.0
	account := domain.Account{ID: "codex:test", ProviderID: "codex", Label: "Work", Source: "CODEX_HOME"}
	previous := domain.Snapshot{
		AccountID: account.ID, FetchedAt: time.Now().Add(-time.Minute), Status: domain.StatusFresh,
		Windows: []domain.QuotaWindow{{ID: "primary", Label: "Primary", UsedPercent: &used}},
	}
	if err := database.Save(t.Context(), account, previous); err != nil {
		t.Fatal(err)
	}

	provider := failingProvider{
		candidate: domain.AccountCandidate{ID: account.ID, ProviderID: "codex", Label: account.Label, Source: account.Source},
		err:       &domain.CodedError{Code: "codex_auth_required", Err: errors.New("authentication required")},
	}
	engine := New(database, []domain.Provider{provider}, time.Minute, time.Second, 30)
	if err := engine.Refresh(t.Context()); err == nil {
		t.Fatal("expected refresh to report the account failure")
	}

	state, err := database.Latest(t.Context(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state.Snapshot.Status != domain.StatusAuthError || !state.Snapshot.Stale {
		t.Fatalf("expected stale auth error, got %#v", state.Snapshot)
	}
	if len(state.Snapshot.Windows) != 1 {
		t.Fatalf("expected previous quota windows to remain visible, got %#v", state.Snapshot.Windows)
	}
}
