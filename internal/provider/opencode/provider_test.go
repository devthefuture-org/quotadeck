package opencode

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reply(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func fixture(t *testing.T, expired bool) (config.OpenCodeConfig, *sql.DB, Credential) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.OpenCodeConfig{DatabasePath: filepath.Join(dir, "opencode.db"), StatePath: filepath.Join(dir, "state")}
	db, err := sql.Open("sqlite", cfg.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE credential(id TEXT PRIMARY KEY,integration_id TEXT,label TEXT,value TEXT,active INTEGER,time_created INTEGER,time_updated INTEGER)`)
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Now().Add(time.Hour).UnixMilli()
	if expired {
		expiry = 0
	}
	raw, _ := json.Marshal(map[string]any{"type": "oauth", "methodID": "device", "access": "access-secret", "refresh": "refresh-secret", "expires": expiry, "metadata": map[string]string{"server": consoleURL, "orgID": "org_test"}, "extra": "preserved"})
	_, err = db.Exec(`INSERT INTO credential VALUES('cre_test','opencode','Personal',?,1,0,0)`, string(raw))
	if err != nil {
		t.Fatal(err)
	}
	c, err := LocalCredentials(t.Context(), cfg)
	if err != nil || len(c) != 1 {
		t.Fatalf("discovery: %v", err)
	}
	return cfg, db, c[0]
}
func TestDiscoverAndFetchWithoutLeakingSecrets(t *testing.T) {
	cfg, _, _ := fixture(t, false)
	p := New(cfg)
	p.client = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://opencode.ai/inference/go/v1/usage" || r.Header.Get("Authorization") != "Bearer access-secret" || r.Header.Get("x-opencode-org-id") != "org_test" {
			t.Error("wrong authenticated quota request")
		}
		return reply(200, `{"usage":{"rolling":{"status":"ok","percent":12,"resetsAt":"2026-11-07T12:00:00Z"},"weekly":{"status":"ok","percent":24,"resetsAt":"2026-11-09T00:00:00Z"},"monthly":{"status":"rate-limited","percent":100,"resetsAt":"2026-12-06T07:21:46Z"}}}`), nil
	})}
	candidates, err := p.Discover(t.Context())
	if err != nil || len(candidates) != 1 {
		t.Fatal(err)
	}
	account, snapshot, err := p.Fetch(t.Context(), candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Windows) != 3 || *snapshot.Windows[0].UsedPercent != 12 || snapshot.Windows[2].ID != "monthly" || !account.Active {
		t.Fatalf("invalid quotas: %+v", snapshot)
	}
	raw, _ := json.Marshal([]any{candidates, account, snapshot})
	if strings.Contains(string(raw), "access-secret") || strings.Contains(string(raw), "refresh-secret") {
		t.Fatal("credential leaked")
	}
}
func TestRefreshUsesOpenCodeLockAndPreservesFields(t *testing.T) {
	_, db, c := fixture(t, true)
	var calls atomic.Int32
	c.client = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.String() != consoleURL+"/auth/device/token" || body["refresh_token"] != "refresh-secret" || body["client_id"] != "opencode-cli" {
			t.Error("wrong refresh request")
		}
		return reply(200, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"org_id":"org_test"}`), nil
	})}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := c.Token(context.Background())
			if err != nil || token != "new-access" {
				t.Errorf("refresh failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent token rotation: %d", calls.Load())
	}
	var raw string
	db.QueryRow(`SELECT value FROM credential WHERE id='cre_test'`).Scan(&raw)
	var v map[string]any
	json.Unmarshal([]byte(raw), &v)
	if v["extra"] != "preserved" || v["refresh"] != "new-refresh" {
		t.Fatal("refresh did not preserve credential fields")
	}
}
func TestRefreshRejectsErrorsAndConcurrentLogout(t *testing.T) {
	for _, status := range []int{400, 401, 403, 500, 302, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			_, db, c := fixture(t, true)
			c.client = &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
				return reply(status, `{"access_token":"bad","refresh_token":"bad","expires_in":0,"error":"refresh-secret"}`), nil
			})}
			_, err := c.Token(t.Context())
			if err == nil || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe error: %v", err)
			}
			var raw string
			db.QueryRow(`SELECT value FROM credential`).Scan(&raw)
			if !strings.Contains(raw, "refresh-secret") {
				t.Fatal("failed refresh changed credentials")
			}
		})
	}
	_, db, c := fixture(t, true)
	c.client = &http.Client{Transport: transport(func(*http.Request) (*http.Response, error) {
		db.Exec(`DELETE FROM credential`)
		return reply(200, `{"access_token":"new","refresh_token":"new","expires_in":3600}`), nil
	})}
	if _, err := c.Token(t.Context()); !errors.Is(err, ErrLoginRequired) {
		t.Fatal("logout was not respected")
	}
}
func TestDiscoveryRejectsForeignServersAndInvalidSchema(t *testing.T) {
	cfg, db, _ := fixture(t, false)
	db.Exec(`UPDATE credential SET value=replace(value,'https://opencode.ai/console','https://example.org')`)
	c, err := LocalCredentials(t.Context(), cfg)
	if err != nil || len(c) != 0 {
		t.Fatal("foreign credential accepted")
	}
}
func TestIncompleteUsageIsNotZero(t *testing.T) {
	for _, payload := range []string{`{}`, `{"usage":{}}`, `{"usage":{"rolling":{"percent":0}}}`, `invalid`} {
		if _, err := parseUsage(strings.NewReader(payload)); err == nil {
			t.Fatal("incomplete quotas reported as valid")
		}
	}
}
func TestBusyLockAndRelease(t *testing.T) {
	_, _, c := fixture(t, true)
	release, err := c.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := c.lock(ctx); err == nil {
		t.Fatal("concurrent lock acquired")
	}
	release()
	release, err = c.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestOpenCodeStaleLockIsRecovered(t *testing.T) {
	_, _, c := fixture(t, true)
	sum := sha1.Sum([]byte("credential-refresh:" + c.ID))
	path := filepath.Join(c.State, "locks", hex.EncodeToString(sum[:])+".lock")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	heartbeat := filepath.Join(path, "heartbeat")
	os.WriteFile(heartbeat, nil, 0o600)
	old := time.Now().Add(-2 * time.Minute)
	os.Chtimes(heartbeat, old, old)
	release, err := c.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lock remained after release")
	}
}
