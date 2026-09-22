package codex

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/devthefuture-org/quotadeck/internal/domain"
)

// The wording Codex 0.153.4 returns once the ChatGPT refresh token is revoked.
const expiredSessionMessage = `failed to fetch codex rate limits: GET https://chatgpt.com/backend-api/wham/usage failed: 401 Unauthorized; content-type=text/plain; body={
  "error": {
    "message": "Provided authentication token is expired. Please try signing in again.",
    "type": null,
    "code": "token_expired",
    "param": null
  },
  "status": 401
}`

func TestRPCFailureClassifiesExpiredSession(t *testing.T) {
	err := rpcFailure("/home/user/.codex", func() string { return "" }, &rpcError{Code: -32603, Message: expiredSessionMessage})

	if code := domain.ErrorCode(err); code != "codex_auth_required" {
		t.Fatalf("expected codex_auth_required, got %q (%v)", code, err)
	}
	// The actionable part must lead: the poller truncates the message at 240 bytes.
	if !strings.HasPrefix(err.Error(), "Codex session expired for CODEX_HOME /home/user/.codex, run `codex login`") {
		t.Fatalf("remediation is not at the head of the message: %v", err)
	}
	if !strings.Contains(err.Error(), "token_expired") {
		t.Fatalf("upstream wording was dropped: %v", err)
	}
}

func TestRPCFailureKeepsUnknownUpstreamWording(t *testing.T) {
	err := rpcFailure("/home/user/.codex", func() string { return "" }, &rpcError{Code: -32603, Message: "backend is on fire"})

	if code := domain.ErrorCode(err); code != "codex_rpc_failed" {
		t.Fatalf("expected codex_rpc_failed, got %q", code)
	}
	if !strings.Contains(err.Error(), "backend is on fire") {
		t.Fatalf("an unrecognised message must still reach the user, got %v", err)
	}
	if !strings.Contains(err.Error(), "-32603") {
		t.Fatalf("expected the JSON-RPC code in %v", err)
	}
}

func TestRPCFailureFallsBackToStderrWhenStdoutSaysNothing(t *testing.T) {
	stderr := &syncBuffer{}
	stderr.Write([]byte("\x1b[2m2026-09-22T07:05:55Z\x1b[0m \x1b[31mERROR\x1b[0m codex_login::auth::manager: Failed to refresh token: 401 Unauthorized: \"Invalid refresh token.\"\n"))

	err := rpcFailure("/home/user/.codex", stderr.lastLine, errors.New("Codex app-server closed before responding"))

	if code := domain.ErrorCode(err); code != "codex_auth_required" {
		t.Fatalf("expected the stderr tail to classify the failure, got %q (%v)", code, err)
	}
	if strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("ANSI styling leaked into the message: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "Invalid refresh token.") {
		t.Fatalf("expected the stderr cause in %v", err)
	}
}

func TestRPCFailureKeepsCrashCauseFromStderr(t *testing.T) {
	stderr := &syncBuffer{}
	stderr.Write([]byte("thread 'main' panicked at src/main.rs:12\n"))

	err := rpcFailure("/home/user/.codex", stderr.lastLine, errors.New("Codex app-server closed before responding"))

	if code := domain.ErrorCode(err); code != "codex_rpc_failed" {
		t.Fatalf("expected codex_rpc_failed, got %q", code)
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("expected the crash cause in %v", err)
	}
}

func TestSyncBufferStaysBounded(t *testing.T) {
	stderr := &syncBuffer{}
	written, err := stderr.Write([]byte(strings.Repeat("x", stderrCapacity+4096)))
	if err != nil || written != stderrCapacity+4096 {
		t.Fatalf("Write must report the full length it accepted, got %d, %v", written, err)
	}
	if got := stderr.buffer.Len(); got != stderrCapacity {
		t.Fatalf("expected the buffer capped at %d, got %d", stderrCapacity, got)
	}
}

func TestTruncateDoesNotSplitRunes(t *testing.T) {
	if got := truncate(strings.Repeat("é", 10), 5); !utf8.ValidString(got) {
		t.Fatalf("truncate produced invalid UTF-8: %q", got)
	}
}
