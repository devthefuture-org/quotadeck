package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/doctor"
	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/poller"
	"github.com/devthefuture-org/quotadeck/internal/store"
)

func TestStateAndRefreshCSRFGuard(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account := domain.Account{ID: "test", ProviderID: "claude", Label: "test", Source: "fixture"}
	snapshot := domain.Snapshot{AccountID: account.ID, FetchedAt: time.Now().UTC(), Status: domain.StatusFresh, Windows: []domain.QuotaWindow{}}
	if err := database.Save(context.Background(), account, snapshot); err != nil {
		t.Fatal(err)
	}
	engine := poller.New(database, nil, time.Minute, time.Second, 30)
	server := New(engine, doctor.Collector{Config: config.Default(), Version: "test"})

	request := httptest.NewRequest(http.MethodGet, "/api/v1/state", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("state returned %d: %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/refresh", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("refresh without CSRF header returned %d", recorder.Code)
	}
}
