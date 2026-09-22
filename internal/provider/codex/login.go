package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LoginMode selects how the user proves their identity to ChatGPT.
type LoginMode string

const (
	// LoginBrowser hands back a URL to open. Codex hosts the OAuth callback on
	// a fixed local port, which makes this mode a globally scarce resource.
	LoginBrowser LoginMode = "browser"
	// LoginDeviceCode hands back a short code to type on another page. It binds
	// no local port, so it always works, including while a browser login runs.
	LoginDeviceCode LoginMode = "deviceCode"
)

type LoginState string

const (
	LoginStarting  LoginState = "starting"
	LoginPending   LoginState = "pending"
	LoginCompleted LoginState = "completed"
	LoginFailed    LoginState = "failed"
	LoginCanceled  LoginState = "canceled"
	LoginExpired   LoginState = "expired"
)

func (s LoginState) terminal() bool {
	return s == LoginCompleted || s == LoginFailed || s == LoginCanceled || s == LoginExpired
}

var (
	ErrLoginBrowserBusy = errors.New("a browser sign-in is already running")
	ErrLoginUnknown     = errors.New("no such sign-in")
	ErrLoginClosed      = errors.New("QuotaDeck is shutting down")
)

// Codex allows 10 minutes for the browser callback and 15 for device-code
// polling. Our own ceilings sit above each so Codex's own expiry is what the
// user sees, not a QuotaDeck deadline that fires first.
const (
	browserLifetime    = 10 * time.Minute
	deviceCodeLifetime = 15 * time.Minute
	lifetimeMargin     = time.Minute
	// Starting is bounded separately: device-code start makes an HTTP call
	// before it answers, and a hung one must not occupy the slot for 15 minutes.
	startTimeout = 45 * time.Second
	// A terminal result outlives its session so a client that reconnects, or
	// reloads the page, still learns how the sign-in ended.
	retentionPeriod = 5 * time.Minute
)

// LoginSnapshot is the whole truth about one sign-in, safe to copy and publish.
type LoginSnapshot struct {
	ID              string     `json:"loginId"`
	AccountID       string     `json:"accountId"`
	Mode            LoginMode  `json:"mode"`
	State           LoginState `json:"state"`
	AuthURL         string     `json:"authUrl,omitempty"`
	VerificationURL string     `json:"verificationUrl,omitempty"`
	UserCode        string     `json:"userCode,omitempty"`
	Error           string     `json:"error,omitempty"`
	StartedAt       time.Time  `json:"startedAt"`
	ExpiresAt       time.Time  `json:"expiresAt"`
}

// LoginManager runs Codex sign-in sessions, which outlive the HTTP request that
// starts them. It is owned by the application runtime, not by a handler.
type LoginManager struct {
	provider *Provider
	publish  func(LoginSnapshot)
	// refresh is asked to re-read quotas once a sign-in succeeds. It must be a
	// guaranteed, coalescing refresh: a best-effort one is dropped whenever the
	// regular poll happens to hold the provider lock.
	refresh func()

	mu            sync.Mutex
	byHome        map[string]*loginSession
	byID          map[string]*loginSession
	retained      map[string]LoginSnapshot
	browserActive bool
	closed        bool

	wg sync.WaitGroup
}

type loginSession struct {
	manager  *LoginManager
	conn     *conn
	home     string
	snapshot LoginSnapshot
	cancel   context.CancelFunc
	done     chan struct{}
}

func NewLoginManager(provider *Provider, publish func(LoginSnapshot), refresh func()) *LoginManager {
	if publish == nil {
		publish = func(LoginSnapshot) {}
	}
	if refresh == nil {
		refresh = func() {}
	}
	return &LoginManager{
		provider: provider, publish: publish, refresh: refresh,
		byHome:   make(map[string]*loginSession),
		byID:     make(map[string]*loginSession),
		retained: make(map[string]LoginSnapshot),
	}
}

// canonicalHome identifies a CODEX_HOME the way Codex itself does. Two symlinks
// to one directory must not look like two independent homes, or the exclusion
// that protects auth.json would not hold.
func canonicalHome(home string) string {
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		return resolved
	}
	if absolute, err := filepath.Abs(home); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(home)
}

// Start begins a sign-in, or returns the one already running for this home.
// Returning the live session rather than replacing it keeps a double click from
// making Codex abandon the first attempt and report it as a failure.
func (m *LoginManager) Start(ctx context.Context, accountID, home string, mode LoginMode) (LoginSnapshot, error) {
	canonical := canonicalHome(home)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return LoginSnapshot{}, ErrLoginClosed
	}
	if existing, ok := m.byHome[canonical]; ok {
		snapshot := existing.snapshot
		m.mu.Unlock()
		return snapshot, nil
	}
	if mode == LoginBrowser && m.browserActive {
		m.mu.Unlock()
		return LoginSnapshot{}, ErrLoginBrowserBusy
	}
	m.mu.Unlock()

	// Hold the home against the poller for the whole session: a rate-limit read
	// refreshes and rewrites the tokens this sign-in is about to replace.
	release, ok := m.provider.homes.tryAcquire(canonical)
	if !ok {
		return LoginSnapshot{}, fmt.Errorf("a Codex refresh is using %s, retry in a moment", home)
	}

	sessionCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	connection, err := openConn(sessionCtx, m.provider.binary, home)
	if err != nil {
		cancel()
		release()
		return LoginSnapshot{}, err
	}
	session := &loginSession{
		manager: m, conn: connection, home: canonical, cancel: cancel,
		done: make(chan struct{}),
		snapshot: LoginSnapshot{
			ID: "pending:" + canonical, AccountID: accountID, Mode: mode,
			State: LoginStarting, StartedAt: time.Now().UTC(),
		},
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		connection.close()
		release()
		return LoginSnapshot{}, ErrLoginClosed
	}
	m.byHome[canonical] = session
	if mode == LoginBrowser {
		m.browserActive = true
	}
	m.mu.Unlock()

	// Completions are collected from the moment the connection opens: Codex
	// spawns its completion task before it answers login/start, so the
	// notification can legitimately arrive first.
	completions := make(chan loginCompletion, 4)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		session.collectCompletions(completions)
	}()

	handle, err := session.begin(mode)
	if err != nil {
		session.finish(LoginFailed, err.Error(), release)
		return LoginSnapshot{}, err
	}

	m.mu.Lock()
	delete(m.byID, session.snapshot.ID)
	session.snapshot = handle
	m.byID[handle.ID] = session
	snapshot := session.snapshot
	m.mu.Unlock()
	m.publish(snapshot)

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		session.await(sessionCtx, completions, release)
	}()
	return snapshot, nil
}

type loginCompletion struct {
	LoginID string `json:"loginId"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

func (s *loginSession) collectCompletions(out chan<- loginCompletion) {
	for note := range s.conn.notifications() {
		if note.Method != "account/login/completed" {
			continue
		}
		var payload loginCompletion
		if err := json.Unmarshal(note.Params, &payload); err != nil {
			continue
		}
		select {
		case out <- payload:
		default:
		}
	}
}

func (s *loginSession) begin(mode LoginMode) (LoginSnapshot, error) {
	if _, err := s.conn.call(1, "initialize", map[string]any{
		"clientInfo": map[string]string{"name": "quotadeck", "title": "QuotaDeck", "version": "0.1.0"},
	}); err != nil {
		return LoginSnapshot{}, rpcFailure(s.home, s.conn.stderrTail, err)
	}
	if err := s.conn.send(map[string]any{"method": "initialized"}); err != nil {
		return LoginSnapshot{}, rpcFailure(s.home, s.conn.stderrTail, errors.New("write Codex request"))
	}
	params := map[string]any{"type": "chatgpt"}
	lifetime := browserLifetime
	if mode == LoginDeviceCode {
		params = map[string]any{"type": "chatgptDeviceCode"}
		lifetime = deviceCodeLifetime
	}
	result, err := s.callWithin(startTimeout, 2, "account/login/start", params)
	if err != nil {
		return LoginSnapshot{}, rpcFailure(s.home, s.conn.stderrTail, err)
	}
	var payload struct {
		LoginID         string `json:"loginId"`
		AuthURL         string `json:"authUrl"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if err := json.Unmarshal(result, &payload); err != nil || strings.TrimSpace(payload.LoginID) == "" {
		return LoginSnapshot{}, &rpcError{Code: -32603, Message: "Codex returned no sign-in handle"}
	}
	snapshot := s.snapshot
	snapshot.ID = payload.LoginID
	snapshot.State = LoginPending
	snapshot.AuthURL = payload.AuthURL
	snapshot.VerificationURL = payload.VerificationURL
	snapshot.UserCode = payload.UserCode
	snapshot.ExpiresAt = snapshot.StartedAt.Add(lifetime)
	return snapshot, nil
}

// callWithin bounds a single request. The connection wakes waiters when the
// child dies, but a child that stays alive and simply never answers would
// otherwise hold the session open.
func (s *loginSession) callWithin(limit time.Duration, id int, method string, params any) (json.RawMessage, error) {
	type outcome struct {
		result json.RawMessage
		err    error
	}
	results := make(chan outcome, 1)
	go func() {
		result, err := s.conn.call(id, method, params)
		results <- outcome{result, err}
	}()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	select {
	case got := <-results:
		return got.result, got.err
	case <-timer.C:
		return nil, errors.New("Codex did not answer the sign-in request in time")
	}
}

func (s *loginSession) await(ctx context.Context, completions <-chan loginCompletion, release func()) {
	lifetime := time.Until(s.snapshot.ExpiresAt) + lifetimeMargin
	timer := time.NewTimer(lifetime)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			s.finish(LoginCanceled, "", release)
			return
		case <-timer.C:
			s.finish(LoginExpired, "the sign-in was not completed in time", release)
			return
		case completion := <-completions:
			// Codex guards its own cleanup by login id; so do we, or a stale
			// session would conclude on behalf of the one that replaced it.
			if completion.LoginID != "" && completion.LoginID != s.snapshot.ID {
				continue
			}
			if completion.Success {
				s.finish(LoginCompleted, "", release)
				return
			}
			s.finish(LoginFailed, completion.Error, release)
			return
		}
	}
}

// finish performs the single terminal transition of a session. Everything that
// must happen once — releasing the home, freeing the browser slot, publishing,
// asking for a refresh — happens here and nowhere else.
func (s *loginSession) finish(state LoginState, message string, release func()) {
	manager := s.manager
	manager.mu.Lock()
	if s.snapshot.State.terminal() {
		manager.mu.Unlock()
		return
	}
	s.snapshot.State = state
	s.snapshot.Error = message
	// The handles are single-use secrets in effect; do not keep serving them
	// once the session is over.
	s.snapshot.AuthURL = ""
	s.snapshot.UserCode = ""
	s.snapshot.VerificationURL = ""
	snapshot := s.snapshot
	if current, ok := manager.byHome[s.home]; ok && current == s {
		delete(manager.byHome, s.home)
	}
	delete(manager.byID, snapshot.ID)
	manager.retained[s.home] = snapshot
	if s.snapshot.Mode == LoginBrowser {
		manager.browserActive = false
	}
	manager.mu.Unlock()

	s.cancel()
	s.conn.close()
	release()
	close(s.done)

	manager.publish(snapshot)
	if state == LoginCompleted {
		manager.refresh()
	}
	time.AfterFunc(retentionPeriod, func() {
		manager.mu.Lock()
		if kept, ok := manager.retained[s.home]; ok && kept.ID == snapshot.ID {
			delete(manager.retained, s.home)
		}
		manager.mu.Unlock()
	})
}

// Cancel ends a sign-in. It tells Codex first so the callback server is released
// promptly, but never waits on that answer: the teardown must happen either way.
func (m *LoginManager) Cancel(loginID string) error {
	m.mu.Lock()
	session, ok := m.byID[loginID]
	m.mu.Unlock()
	if !ok {
		return ErrLoginUnknown
	}
	go func() {
		_, _ = session.callWithin(5*time.Second, 9, "account/login/cancel",
			map[string]any{"loginId": loginID})
		session.cancel()
	}()
	select {
	case <-session.done:
	case <-time.After(6 * time.Second):
		session.cancel()
	}
	return nil
}

// Current reports the live session for a home, or the retained result of the
// one that just ended. A client that reloaded the page has lost the login id,
// so the home is the durable key, not the id.
func (m *LoginManager) Current(home string) (LoginSnapshot, bool) {
	canonical := canonicalHome(home)
	m.mu.Lock()
	defer m.mu.Unlock()
	if session, ok := m.byHome[canonical]; ok {
		return session.snapshot, true
	}
	if snapshot, ok := m.retained[canonical]; ok {
		return snapshot, true
	}
	return LoginSnapshot{}, false
}

// Close ends every session and waits for them. The runtime calls it before
// closing the store, so nothing is still writing when the database goes away.
func (m *LoginManager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.wg.Wait()
		return
	}
	m.closed = true
	sessions := make([]*loginSession, 0, len(m.byHome))
	for _, session := range m.byHome {
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	for _, session := range sessions {
		session.cancel()
	}
	m.wg.Wait()
}
