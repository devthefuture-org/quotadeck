//go:build unix

package codex

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/domain"
)

func TestRPCReleasesPipesInheritedByLauncherChild(t *testing.T) {
	for _, responds := range []bool{false, true} {
		name := "timeout"
		if responds {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			child := filepath.Join(home, "child")
			script := "#!/bin/sh\necho $$ > \"$CODEX_HOME/child.pid\"\n"
			if responds {
				script += `printf '%s\n' '{"id":1,"result":{}}' '{"id":2,"result":{"account":{}}}' '{"id":3,"result":{"rateLimits":{}}}'` + "\n"
			}
			script += "exec sleep 60\n"
			if err := os.WriteFile(child, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(home, "codex")
			if err := os.WriteFile(launcher, []byte("#!/bin/sh\n\"$CODEX_HOME/child\" &\nwait\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			// Also clean up the child when exercising the broken implementation.
			t.Cleanup(func() {
				data, _ := os.ReadFile(filepath.Join(home, "child.pid"))
				pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
				if pid > 0 {
					process, _ := os.FindProcess(pid)
					_ = process.Kill()
				}
			})
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, _, err := New(launcher, nil).rpc(ctx, home)
				done <- err
			}()
			select {
			case err := <-done:
				if responds && err != nil {
					t.Fatalf("completed RPC failed: %v", err)
				}
				if !responds && err == nil {
					t.Fatal("expected a timeout error")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("RPC remained blocked on pipes inherited by the launcher's child")
			}
		})
	}
}

// An expired ChatGPT session reaches quotadeck as a generic JSON-RPC internal
// error on the rate-limit request, after account/read has already succeeded.
func TestRPCReportsExpiredSessionEndToEnd(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "codex")
	// Mirror the real sequencing: quotadeck only sends the remaining requests
	// once initialize has been answered.
	script := "#!/bin/sh\n" +
		"read -r line\n" +
		`printf '%s\n' '{"id":1,"result":{}}'` + "\n" +
		"read -r line\nread -r line\n" +
		`printf '%s\n' '{"id":2,"result":{"account":{"type":"chatgpt","planType":"prolite"},"requiresOpenaiAuth":true}}'` + "\n" +
		"read -r line\n" +
		"echo 'ERROR codex_login::auth::manager: Failed to refresh token' >&2\n" +
		`printf '%s\n' '{"id":3,"error":{"code":-32603,"message":"failed to fetch codex rate limits: GET https://chatgpt.com/backend-api/wham/usage failed: 401 Unauthorized; body={\"code\":\"token_expired\"}"}}'` + "\n" +
		"sleep 5\n"
	if err := os.WriteFile(launcher, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, _, err := New(launcher, nil).rpc(ctx, home)

	if code := domain.ErrorCode(err); code != "codex_auth_required" {
		t.Fatalf("expected codex_auth_required, got %q (%v)", code, err)
	}
	if !strings.Contains(err.Error(), "codex login") {
		t.Fatalf("expected the remediation in %v", err)
	}
}

// A crash before any response leaves stderr as the only account of the cause.
func TestRPCSurfacesStderrWhenAppServerDiesSilently(t *testing.T) {
	home := t.TempDir()
	launcher := filepath.Join(home, "codex")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\necho 'codex: error while loading shared libraries' >&2\nexit 127\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	_, _, err := New(launcher, nil).rpc(ctx, home)

	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "shared libraries") {
		t.Fatalf("stderr cause was dropped: %v", err)
	}
}
