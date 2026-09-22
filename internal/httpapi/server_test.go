package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/devthefuture-org/quotadeck/internal/provider/codex"
	"github.com/devthefuture-org/quotadeck/internal/store"
)

type fakeController struct {
	switched string
	apiKey   string
	activate bool
	setup    control.ClaudeSetupResult
	setupErr error
}

type slowUnrelatedProvider struct{}

func (slowUnrelatedProvider) ID() string   { return "codex" }
func (slowUnrelatedProvider) Name() string { return "Slow Codex" }
func (slowUnrelatedProvider) Discover(ctx context.Context) ([]domain.AccountCandidate, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (slowUnrelatedProvider) Fetch(context.Context, domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	return domain.Account{}, domain.Snapshot{}, nil
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

func (f *fakeController) SetupClaude(context.Context) (control.ClaudeSetupResult, error) {
	return f.setup, f.setupErr
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

func TestClaudeSwitchRespondsWithoutWaitingForUnrelatedProviders(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, account := range []domain.Account{
		{ID: "claude:cswap:1", ProviderID: "claude", Label: "Old", Active: true, Source: "cswap"},
		{ID: "claude:cswap:2", ProviderID: "claude", Label: "New", Source: "cswap"},
	} {
		snapshot := domain.Snapshot{AccountID: account.ID, FetchedAt: time.Now().UTC(), Status: domain.StatusFresh}
		if err := database.Save(t.Context(), account, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	engine := poller.New(database, []domain.Provider{slowUnrelatedProvider{}}, time.Minute, 500*time.Millisecond, 30)
	controller := &fakeController{}
	server := New(engine, doctor.Collector{Config: config.Default(), Version: "test"}, controller)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/control/claude/switch", bytes.NewBufferString(`{"accountId":"claude:cswap:2"}`))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-QuotaDeck-Request", "control")
	started := time.Now()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if elapsed := time.Since(started); elapsed >= 250*time.Millisecond {
		t.Fatalf("switch waited for an unrelated provider: %s", elapsed)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("switch returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Refresh string         `json:"refresh"`
		Control control.Status `json:"control"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Refresh != "queued" || response.Control.Claude.ActiveAccountID != "claude:cswap:2" {
		t.Fatalf("unexpected immediate control response: %#v", response)
	}
}

func TestClaudeSetupRequiresGuardAndReturnsRedactedResult(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	engine := poller.New(database, nil, time.Minute, time.Second, 30)
	controller := &fakeController{setup: control.ClaudeSetupResult{Installed: true, AccountAdded: true, AccountCount: 1}}
	server := New(engine, doctor.Collector{Config: config.Default(), Version: "test"}, controller)

	request := httptest.NewRequest(http.MethodPost, "/api/v1/control/claude/setup", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("setup without guard returned %d", recorder.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/control/claude/setup", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-QuotaDeck-Request", "control")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("setup returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if !bytes.Contains(recorder.Body.Bytes(), []byte(`"accountCount":1`)) {
		t.Fatalf("setup result missing: %s", recorder.Body.String())
	}
}

// fakeCodexProvider reports one configured account, so the handler can resolve
// an id to a CODEX_HOME the way the real provider does.
type fakeCodexProvider struct{}

func (fakeCodexProvider) ID() string   { return "codex" }
func (fakeCodexProvider) Name() string { return "Codex" }
func (fakeCodexProvider) Discover(context.Context) ([]domain.AccountCandidate, error) {
	return []domain.AccountCandidate{{
		ID: "codex:home:known", ProviderID: "codex", Label: "Codex",
		Source: "CODEX_HOME", Ref: "/home/user/.codex",
	}}, nil
}
func (fakeCodexProvider) Fetch(context.Context, domain.AccountCandidate) (domain.Account, domain.Snapshot, error) {
	return domain.Account{}, domain.Snapshot{}, nil
}

type fakeLogins struct {
	startedHome string
	startedMode codex.LoginMode
	canceled    string
	err         error
}

func (f *fakeLogins) Start(_ context.Context, accountID, home string, mode codex.LoginMode) (codex.LoginSnapshot, error) {
	if f.err != nil {
		return codex.LoginSnapshot{}, f.err
	}
	f.startedHome, f.startedMode = home, mode
	return codex.LoginSnapshot{ID: "login-1", AccountID: accountID, Mode: mode, State: codex.LoginPending}, nil
}

func (f *fakeLogins) Cancel(loginID string) error {
	f.canceled = loginID
	return nil
}

func (f *fakeLogins) Current(string) (codex.LoginSnapshot, bool) {
	return codex.LoginSnapshot{ID: "login-1", State: codex.LoginPending}, true
}

func codexLoginServer(t *testing.T, logins CodexLogins) *Server {
	t.Helper()
	database, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	engine := poller.New(database, []domain.Provider{fakeCodexProvider{}}, time.Minute, time.Second, 30)
	return New(engine, doctor.Collector{Config: config.Default(), Version: "test"}, &fakeController{}).
		WithCodexLogins(logins)
}

func codexLoginRequest(method, target string, body any) *http.Request {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(payload))
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-QuotaDeck-Request", "control")
	return request
}

func TestCodexLoginRefusesWithoutLoopbackOrGuard(t *testing.T) {
	server := codexLoginServer(t, &fakeLogins{})
	body := map[string]string{"accountId": "codex:home:known", "mode": "browser"}

	remote := codexLoginRequest(http.MethodPost, "/api/v1/control/codex/login", body)
	remote.RemoteAddr = "203.0.113.7:9999"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, remote)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a non-loopback sign-in returned %d", recorder.Code)
	}

	unguarded := codexLoginRequest(http.MethodPost, "/api/v1/control/codex/login", body)
	unguarded.Header.Del("X-QuotaDeck-Request")
	recorder = httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, unguarded)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("a sign-in without the control header returned %d", recorder.Code)
	}
}

// The CODEX_HOME must come from the resolved account, never from the caller:
// otherwise the endpoint runs Codex against any path it is handed.
func TestCodexLoginTakesTheHomeFromTheResolvedAccount(t *testing.T) {
	logins := &fakeLogins{}
	server := codexLoginServer(t, logins)

	request := codexLoginRequest(http.MethodPost, "/api/v1/control/codex/login", map[string]string{
		"accountId": "codex:home:known", "mode": "deviceCode",
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("sign-in returned %d: %s", recorder.Code, recorder.Body.String())
	}
	if logins.startedHome != "/home/user/.codex" {
		t.Fatalf("expected the discovered home, got %q", logins.startedHome)
	}
	if logins.startedMode != codex.LoginDeviceCode {
		t.Fatalf("expected the requested mode, got %q", logins.startedMode)
	}
}

func TestCodexLoginRejectsAnAccountThatIsNotConfigured(t *testing.T) {
	server := codexLoginServer(t, &fakeLogins{})

	request := codexLoginRequest(http.MethodPost, "/api/v1/control/codex/login", map[string]string{
		"accountId": "codex:home:retired", "mode": "browser",
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("an unconfigured account returned %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestCodexLoginRejectsAnUnknownMode(t *testing.T) {
	server := codexLoginServer(t, &fakeLogins{})

	request := codexLoginRequest(http.MethodPost, "/api/v1/control/codex/login", map[string]string{
		"accountId": "codex:home:known", "mode": "telepathy",
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("an unknown mode returned %d", recorder.Code)
	}
}

// A second browser sign-in is refused with a code the UI can act on, because
// the alternative — the device code — is the remedy.
func TestCodexLoginReportsABusyBrowserSlot(t *testing.T) {
	server := codexLoginServer(t, &fakeLogins{err: codex.ErrLoginBrowserBusy})

	request := codexLoginRequest(http.MethodPost, "/api/v1/control/codex/login", map[string]string{
		"accountId": "codex:home:known", "mode": "browser",
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusConflict {
		t.Fatalf("a busy browser slot returned %d", recorder.Code)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	if payload.Error.Code != "browser_login_busy" {
		t.Fatalf("unexpected error code %q", payload.Error.Code)
	}
}

// The status endpoint is keyed by account: a reloaded page has forgotten the
// sign-in id, and the event stream drops events under load.
func TestCodexLoginStatusIsKeyedByAccount(t *testing.T) {
	server := codexLoginServer(t, &fakeLogins{})

	request := codexLoginRequest(http.MethodGet,
		"/api/v1/control/codex/login?accountId=codex:home:known", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status returned %d: %s", recorder.Code, recorder.Body.String())
	}
	var snapshot codex.LoginSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != "login-1" || snapshot.State != codex.LoginPending {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
}

func TestCodexLoginCancelPassesTheID(t *testing.T) {
	logins := &fakeLogins{}
	server := codexLoginServer(t, logins)

	request := codexLoginRequest(http.MethodDelete, "/api/v1/control/codex/login/login-1", nil)
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || logins.canceled != "login-1" {
		t.Fatalf("cancel returned %d, canceled %q", recorder.Code, logins.canceled)
	}
}

// Without a manager wired the endpoints must refuse rather than panic.
func TestCodexLoginUnavailableWithoutAManager(t *testing.T) {
	database, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	engine := poller.New(database, []domain.Provider{fakeCodexProvider{}}, time.Minute, time.Second, 30)
	server := New(engine, doctor.Collector{Config: config.Default(), Version: "test"})

	request := codexLoginRequest(http.MethodPost, "/api/v1/control/codex/login", map[string]string{
		"accountId": "codex:home:known",
	})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 without a login manager, got %d", recorder.Code)
	}
}
