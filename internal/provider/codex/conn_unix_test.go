//go:build unix

package codex

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeAppServer writes a shell script that answers `initialize` and then emits
// the given lines, mimicking how the real app-server is driven: it reads the
// requests quotadeck writes before replying.
func fakeAppServer(t *testing.T, readsBeforeReply int, lines ...string) (home, launcher string) {
	t.Helper()
	home = t.TempDir()
	launcher = filepath.Join(home, "codex")
	script := "#!/bin/sh\nread -r line\n" + `printf '%s\n' '{"id":1,"result":{}}'` + "\n"
	script += strings.Repeat("read -r line\n", readsBeforeReply)
	for _, line := range lines {
		script += "printf '%s\\n' '" + line + "'\n"
	}
	script += "sleep 5\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return home, launcher
}

// A notification arriving between the two responses must not consume, delay or
// drop either of them.
func TestConnRoutesNotificationsWithoutLosingResponses(t *testing.T) {
	home, launcher := fakeAppServer(t, 3,
		`{"method":"remoteControl/status/changed","params":{"status":"disabled"}}`,
		`{"id":3,"result":{"rateLimits":{"planType":"plus"}}}`,
		`{"method":"account/login/completed","params":{"success":true}}`,
		`{"id":2,"result":{"account":{"type":"chatgpt","planType":"plus"}}}`,
	)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	accountResult, limitsResult, err := New(launcher, nil).rpc(ctx, home)

	if err != nil {
		t.Fatalf("interleaved notifications broke the exchange: %v", err)
	}
	if !strings.Contains(string(accountResult), "chatgpt") {
		t.Fatalf("account response lost: %s", accountResult)
	}
	if !strings.Contains(string(limitsResult), "plus") {
		t.Fatalf("limits response lost: %s", limitsResult)
	}
}

// A caller that never drains notifications must not stall the reader, so the
// responses still arrive even past the notification buffer.
func TestConnNeverBlocksOnUndrainedNotifications(t *testing.T) {
	flood := make([]string, 0, notificationBuffer*3+2)
	for range notificationBuffer * 3 {
		flood = append(flood, `{"method":"remoteControl/status/changed","params":{"status":"disabled"}}`)
	}
	flood = append(flood,
		`{"id":2,"result":{"account":{"type":"chatgpt","planType":"plus"}}}`,
		`{"id":3,"result":{"rateLimits":{"planType":"plus"}}}`,
	)
	home, launcher := fakeAppServer(t, 3, flood...)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, _, err := New(launcher, nil).rpc(ctx, home)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected the exchange to complete, got %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the reader stalled on notifications nobody drained")
	}
}

// EOF after only one response must wake the waiter still pending instead of
// blocking it forever.
func TestConnWakesPendingWaitersOnEOF(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "codex")
	script := "#!/bin/sh\nread -r line\n" + `printf '%s\n' '{"id":1,"result":{}}'` + "\n" +
		"read -r line\nread -r line\nread -r line\n" +
		`printf '%s\n' '{"id":2,"result":{"account":{"type":"chatgpt"}}}'` + "\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, _, err := New(launcher, nil).rpc(ctx, home)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error when the stream ends with a request unanswered")
		}
		if !strings.Contains(err.Error(), "closed before responding") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("a waiter was left hanging after the app-server closed")
	}
}

// A request registered after the stream has already failed must fail too,
// rather than wait for a response that can no longer come.
func TestConnFailsRegistrationsMadeAfterTheStreamEnded(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "codex")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	connection, err := openConn(ctx, launcher, home)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.close()
	<-connection.done

	select {
	case result := <-connection.expect(42):
		if result.err == nil {
			t.Fatal("expected a late registration to fail immediately")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a registration made after the stream ended blocked")
	}
}

// A child that answers before quotadeck has registered its waiter — the fake
// app-servers in this package do exactly that — must not lose its answers.
func TestConnKeepsAnswersThatOutrunTheirRegistration(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "codex")
	script := "#!/bin/sh\n" +
		`printf '%s\n' '{"id":1,"result":{}}' '{"id":2,"result":{"account":{"type":"chatgpt"}}}' '{"id":3,"result":{"rateLimits":{}}}'` + "\n" +
		"sleep 5\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	accountResult, _, err := New(launcher, nil).rpc(ctx, home)

	if err != nil {
		t.Fatalf("answers sent before registration were lost: %v", err)
	}
	if !strings.Contains(string(accountResult), "chatgpt") {
		t.Fatalf("unexpected account response: %s", accountResult)
	}
}

// The existing pipe test kills the child itself during cleanup, so it cannot
// witness that the implementation disposed of it. Assert the disappearance
// before cleanup runs.
func TestConnLeavesNoSurvivingChild(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "codex")
	script := "#!/bin/sh\necho $$ > \"$CODEX_HOME/child.pid\"\n" +
		`printf '%s\n' '{"id":1,"result":{}}' '{"id":2,"result":{"account":{"type":"chatgpt"}}}' '{"id":3,"result":{"rateLimits":{}}}'` + "\n" +
		"exec sleep 60\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	if _, _, err := New(launcher, nil).rpc(ctx, home); err != nil {
		t.Fatalf("exchange failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home, "child.pid"))
	if err != nil {
		t.Fatalf("the fake app-server never recorded its pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		t.Fatalf("unusable pid %q", data)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if err := syscall.Kill(pid, 0); err != nil {
			return // gone, as required
		}
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d survived the exchange", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
