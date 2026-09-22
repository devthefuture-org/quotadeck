//go:build unix

package codex

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/domain"
)

// loginServer writes a fake app-server that answers initialize and
// account/login/start, then emits whatever completion lines are given.
func loginServer(t *testing.T, startReply string, after ...string) (home, launcher string) {
	t.Helper()
	home = t.TempDir()
	launcher = filepath.Join(home, "codex")
	script := "#!/bin/sh\nread -r line\n" +
		`printf '%s\n' '{"id":1,"result":{}}'` + "\n" +
		"read -r line\nread -r line\n" +
		"printf '%s\\n' '" + startReply + "'\n"
	for _, line := range after {
		script += "printf '%s\\n' '" + line + "'\n"
	}
	script += "sleep 30\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return home, launcher
}

func newTestManager(t *testing.T, launcher string) (*LoginManager, chan LoginSnapshot, chan struct{}) {
	t.Helper()
	published := make(chan LoginSnapshot, 16)
	refreshed := make(chan struct{}, 4)
	manager := NewLoginManager(
		New(launcher, nil),
		func(snapshot LoginSnapshot) { published <- snapshot },
		func() { refreshed <- struct{}{} },
	)
	t.Cleanup(manager.Close)
	return manager, published, refreshed
}

func waitForState(t *testing.T, published <-chan LoginSnapshot, state LoginState) LoginSnapshot {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case snapshot := <-published:
			if snapshot.State == state {
				return snapshot
			}
		case <-deadline:
			t.Fatalf("no %q snapshot was published", state)
		}
	}
}

func TestLoginBrowserReturnsTheAuthURL(t *testing.T) {
	home, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgpt","loginId":"abc","authUrl":"https://auth.example.invalid/x"}}`)
	manager, _, _ := newTestManager(t, launcher)

	snapshot, err := manager.Start(t.Context(), "codex:home:test", home, LoginBrowser)

	if err != nil {
		t.Fatal(err)
	}
	if snapshot.State != LoginPending || snapshot.AuthURL != "https://auth.example.invalid/x" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot.ExpiresAt.Sub(snapshot.StartedAt) != browserLifetime {
		t.Fatalf("browser sign-in must expire after %s, got %s", browserLifetime, snapshot.ExpiresAt.Sub(snapshot.StartedAt))
	}
}

func TestLoginDeviceCodeReturnsTheUserCodeAndItsOwnLifetime(t *testing.T) {
	home, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgptDeviceCode","loginId":"dev","verificationUrl":"https://auth.example.invalid/device","userCode":"AAAA-BBBB"}}`)
	manager, _, _ := newTestManager(t, launcher)

	snapshot, err := manager.Start(t.Context(), "codex:home:test", home, LoginDeviceCode)

	if err != nil {
		t.Fatal(err)
	}
	if snapshot.UserCode != "AAAA-BBBB" || snapshot.VerificationURL == "" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	// Codex polls the device code for longer than it waits on the browser
	// callback; a single shared deadline would cut this mode short.
	if snapshot.ExpiresAt.Sub(snapshot.StartedAt) != deviceCodeLifetime {
		t.Fatalf("device code must expire after %s, got %s", deviceCodeLifetime, snapshot.ExpiresAt.Sub(snapshot.StartedAt))
	}
}

func TestLoginSuccessPublishesAndAsksForRefresh(t *testing.T) {
	home, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgpt","loginId":"abc","authUrl":"https://auth.example.invalid/x"}}`,
		`{"method":"account/login/completed","params":{"loginId":"abc","success":true}}`)
	manager, published, refreshed := newTestManager(t, launcher)

	if _, err := manager.Start(t.Context(), "codex:home:test", home, LoginBrowser); err != nil {
		t.Fatal(err)
	}

	final := waitForState(t, published, LoginCompleted)
	if final.AuthURL != "" {
		t.Fatalf("a finished sign-in must stop serving its handle: %#v", final)
	}
	select {
	case <-refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("a successful sign-in must trigger a quota refresh")
	}
}

// Codex reports a sign-in it abandoned as completed with success=false. That
// must reach the user as a failure carrying Codex's own reason.
func TestLoginFailurePublishesTheReportedReason(t *testing.T) {
	home, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgpt","loginId":"abc","authUrl":"https://auth.example.invalid/x"}}`,
		`{"method":"account/login/completed","params":{"loginId":"abc","success":false,"error":"Login was not completed"}}`)
	manager, published, refreshed := newTestManager(t, launcher)

	if _, err := manager.Start(t.Context(), "codex:home:test", home, LoginBrowser); err != nil {
		t.Fatal(err)
	}

	final := waitForState(t, published, LoginFailed)
	if final.Error != "Login was not completed" {
		t.Fatalf("Codex's reason was lost: %#v", final)
	}
	select {
	case <-refreshed:
		t.Fatal("a failed sign-in must not trigger a refresh")
	case <-time.After(300 * time.Millisecond):
	}
}

// A completion for a different sign-in must not conclude this one. The two
// completions disagree on purpose: whichever one the session acts on decides
// the final state, so the outcome names the culprit instead of depending on
// which notification happened to be processed first.
func TestLoginIgnoresCompletionsForAnotherSession(t *testing.T) {
	home, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgpt","loginId":"mine","authUrl":"https://auth.example.invalid/x"}}`,
		`{"method":"account/login/completed","params":{"loginId":"someone-else","success":true}}`,
		`{"method":"account/login/completed","params":{"loginId":"mine","success":false,"error":"mine ended"}}`)
	manager, published, _ := newTestManager(t, launcher)

	if _, err := manager.Start(t.Context(), "codex:home:test", home, LoginBrowser); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case snapshot := <-published:
			if !snapshot.State.terminal() {
				continue
			}
			if snapshot.State != LoginFailed || snapshot.Error != "mine ended" {
				t.Fatalf("a foreign completion decided this sign-in: %#v", snapshot)
			}
			return
		case <-deadline:
			t.Fatal("the sign-in never concluded")
		}
	}
}

// Only one browser sign-in may run at a time: Codex binds a fixed local port
// for the OAuth callback and cancels whatever already holds it.
func TestLoginRefusesASecondBrowserSignIn(t *testing.T) {
	first, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgpt","loginId":"abc","authUrl":"https://auth.example.invalid/x"}}`)
	manager, _, _ := newTestManager(t, launcher)
	if _, err := manager.Start(t.Context(), "codex:home:one", first, LoginBrowser); err != nil {
		t.Fatal(err)
	}

	second := t.TempDir()
	_, err := manager.Start(t.Context(), "codex:home:two", second, LoginBrowser)

	if !errors.Is(err, ErrLoginBrowserBusy) {
		t.Fatalf("expected ErrLoginBrowserBusy, got %v", err)
	}
}

// Starting again on a home that is already signing in returns the live session
// rather than replacing it: replacing would make Codex abandon the first and
// report it as a failure the user never caused.
func TestLoginReturnsTheRunningSessionForTheSameHome(t *testing.T) {
	home, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgpt","loginId":"abc","authUrl":"https://auth.example.invalid/x"}}`)
	manager, _, _ := newTestManager(t, launcher)
	first, err := manager.Start(t.Context(), "codex:home:test", home, LoginBrowser)
	if err != nil {
		t.Fatal(err)
	}

	second, err := manager.Start(t.Context(), "codex:home:test", home, LoginBrowser)

	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("a second start replaced the running sign-in: %q then %q", first.ID, second.ID)
	}
}

// A sign-in holds its CODEX_HOME so the poller cannot rewrite auth.json
// underneath it. Fetch must yield rather than record a failure.
func TestFetchSkipsAHomeThatIsSigningIn(t *testing.T) {
	home, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgpt","loginId":"abc","authUrl":"https://auth.example.invalid/x"}}`)
	provider := New(launcher, nil)
	manager := NewLoginManager(provider, nil, nil)
	t.Cleanup(manager.Close)
	if _, err := manager.Start(t.Context(), "codex:home:test", home, LoginBrowser); err != nil {
		t.Fatal(err)
	}

	_, _, err := provider.Fetch(t.Context(),
		domain.AccountCandidate{ID: "codex:home:test", ProviderID: "codex", Ref: home})

	if !errors.Is(err, domain.ErrSkipAccount) {
		t.Fatalf("expected the fetch to be skipped, got %v", err)
	}
}

// Canceling ends the session promptly even when Codex never answers the cancel.
func TestLoginCancelTearsDownWithoutWaitingOnCodex(t *testing.T) {
	home, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgpt","loginId":"abc","authUrl":"https://auth.example.invalid/x"}}`)
	manager, published, _ := newTestManager(t, launcher)
	snapshot, err := manager.Start(t.Context(), "codex:home:test", home, LoginBrowser)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- manager.Cancel(snapshot.ID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Cancel blocked on an app-server that never answers")
	}
	waitForState(t, published, LoginCanceled)
	if _, ok := manager.Current(home); !ok {
		t.Fatal("the terminal result must be retained for a client that reconnects")
	}
}

func TestLoginCancelRejectsAnUnknownID(t *testing.T) {
	_, launcher := loginServer(t, `{"id":2,"result":{}}`)
	manager, _, _ := newTestManager(t, launcher)

	if err := manager.Cancel("nope"); !errors.Is(err, ErrLoginUnknown) {
		t.Fatalf("expected ErrLoginUnknown, got %v", err)
	}
}

// Close must not leave a session, or its child process, running.
func TestLoginCloseEndsRunningSessions(t *testing.T) {
	home, launcher := loginServer(t,
		`{"id":2,"result":{"type":"chatgpt","loginId":"abc","authUrl":"https://auth.example.invalid/x"}}`)
	manager, _, _ := newTestManager(t, launcher)
	if _, err := manager.Start(t.Context(), "codex:home:test", home, LoginBrowser); err != nil {
		t.Fatal(err)
	}

	finished := make(chan struct{})
	go func() { manager.Close(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("Close did not return: a session was left running")
	}
}
