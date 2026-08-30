package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
	"github.com/devthefuture-org/quotadeck/internal/control"
	"github.com/devthefuture-org/quotadeck/internal/doctor"
	"github.com/devthefuture-org/quotadeck/internal/domain"
	"github.com/devthefuture-org/quotadeck/internal/poller"
	"github.com/devthefuture-org/quotadeck/internal/store"
)

type fakeController struct {
	switched string
	apiKey   string
	activate bool
}

func (f *fakeController) Status(states []domain.AccountState) control.Status {
	status := control.Status{Mode: "claude", ZAI: control.ZAIStatus{Configured: f.apiKey != "", Endpoint: "https://api.z.ai/api/anthropic"}}
	for _, state := range states {
		if state.Account.Active {
			status.Claude.ActiveAccountID = state.Account.ID
		}
	}
	return status
}

func (f *fakeController) SwitchClaude(_ context.Context, accountID string) error {
	f.switched = accountID
	return nil
}

func (f *fakeController) ConfigureZAI(_ context.Context, apiKey string, activate bool) error {
	f.apiKey, f.activate = apiKey, activate
	return nil
}

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

func TestProviderControlsRequireGuardAndNeverEchoZAIKey(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	account := domain.Account{ID: "claude:cswap:2", ProviderID: "claude", Label: "Work", Active: true, Source: "cswap", SourceMeta: map[string]string{"slot": "2"}}
	snapshot := domain.Snapshot{AccountID: account.ID, FetchedAt: time.Now().UTC(), Status: domain.StatusFresh}
	if err := database.Save(t.Context(), account, snapshot); err != nil {
		t.Fatal(err)
	}
	engine := poller.New(database, nil, time.Minute, time.Second, 30)
	controller := &fakeController{}
	server := New(engine, doctor.Collector{Config: config.Default(), Version: "test"}, controller)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/control/claude/switch", bytes.NewBufferString(`{"accountId":"claude:cswap:2"}`))
	request.RemoteAddr = "127.0.0.1:12345"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("switch without guard returned %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/control/claude/switch", bytes.NewBufferString(`{"accountId":"claude:cswap:2"}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-QuotaDeck-Request", "control")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || controller.switched != account.ID {
		t.Fatalf("switch returned %d (%s), selected %q", recorder.Code, recorder.Body.String(), controller.switched)
	}

	secret := "zai-secret-from-browser"
	request = httptest.NewRequest(http.MethodPut, "/api/v1/control/zai", bytes.NewBufferString(`{"apiKey":"`+secret+`","activate":true}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-QuotaDeck-Request", "control")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || controller.apiKey != secret || !controller.activate {
		t.Fatalf("Z.ai configuration returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if bytes.Contains(recorder.Body.Bytes(), []byte(secret)) {
		t.Fatal("Z.ai API response leaked the submitted key")
	}
}
