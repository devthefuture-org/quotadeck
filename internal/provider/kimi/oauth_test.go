package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type oauthTransport func(*http.Request) (*http.Response, error)

func (f oauthTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func localOAuthFixture(t *testing.T, expired bool) LocalCredential {
	t.Helper()
	home := t.TempDir()
	body := `[providers."managed:kimi-code"]
base_url = "https://api.kimi.ai/coding/v1"
[providers."managed:kimi-code".oauth]
storage = "file"
key = "oauth/kimi-code-global"
oauth_host = "https://auth.kimi.ai"
`
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(time.Hour).Unix()
	if expired {
		expires = 1
	}
	payload, _ := json.Marshal(map[string]any{"access_token": "old-access-fixture", "refresh_token": "refresh-fixture", "expires_at": expires, "expires_in": 900, "scope": "kimi-code", "future_field": "keep"})
	if err := os.WriteFile(filepath.Join(home, "credentials", "kimi-code-global.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	credentials := LocalCredentials([]string{home})
	if len(credentials) != 1 {
		t.Fatal("configured local credential was not discovered")
	}
	return credentials[0]
}

func TestLocalOAuthUsesConfiguredRegionAndRefusesUntrustedConfig(t *testing.T) {
	c := localOAuthFixture(t, false)
	if c.Endpoint() != "https://api.kimi.ai/coding/" {
		t.Fatal("global region lost")
	}
	original, _ := os.ReadFile(filepath.Join(c.Home, "config.toml"))
	for _, change := range [][2]string{
		{"https://auth.kimi.ai", "https://auth.kimi.ai.attacker.example"},
		{"https://api.kimi.ai", "https://api.kimi.com"},
		{"oauth/kimi-code-global", "oauth/../outside"},
		{`storage = "file"`, `storage = "keyring"`},
	} {
		if err := os.WriteFile(filepath.Join(c.Home, "config.toml"), []byte(strings.ReplaceAll(string(original), change[0], change[1])), 0o600); err != nil {
			t.Fatal(err)
		}
		if len(LocalCredentials([]string{c.Home})) != 0 {
			t.Fatal("untrusted or unsupported credential accepted")
		}
	}
	c.OAuthHost = "https://attacker.example"
	if _, err := c.Token(t.Context()); err == nil {
		t.Fatal("credential resolver accepted an untrusted host")
	}
}

func TestLocalOAuthReusesAndRereadsFreshToken(t *testing.T) {
	c := localOAuthFixture(t, false)
	c.client = &http.Client{Transport: oauthTransport(func(*http.Request) (*http.Response, error) {
		t.Error("fresh token caused a network call")
		return nil, errors.New("unexpected")
	})}
	if token, err := c.Token(t.Context()); err != nil || token != "old-access-fixture" {
		t.Fatal("fresh token not reused")
	}
	body, _ := os.ReadFile(c.Path())
	if err := os.WriteFile(c.Path(), []byte(strings.ReplaceAll(string(body), "old-access-fixture", "rotated-by-cli-fixture")), 0o600); err != nil {
		t.Fatal(err)
	}
	if token, err := c.Token(t.Context()); err != nil || token != "rotated-by-cli-fixture" {
		t.Fatal("CLI rotation was not picked up")
	}
}

func TestLocalOAuthSerializesRefreshAndPreservesPrivateStore(t *testing.T) {
	c := localOAuthFixture(t, true)
	var calls atomic.Int32
	c.client = &http.Client{Transport: oauthTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://auth.kimi.ai/api/oauth/token" || r.Method != "POST" {
			t.Error("wrong OAuth destination")
		}
		if err := r.ParseForm(); err != nil || r.Form.Get("refresh_token") != "refresh-fixture" || r.Form.Get("grant_type") != "refresh_token" {
			t.Error("wrong refresh form")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access-fixture","refresh_token":"new-refresh-fixture","expires_in":900}`)), Header: make(http.Header)}, nil
	})}
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if token, err := c.Token(context.Background()); err != nil || token != "new-access-fixture" {
				t.Error("concurrent refresh failed", err)
			}
		}()
	}
	group.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d", calls.Load())
	}
	token, raw, err := c.read()
	if err != nil || token.RefreshToken != "new-refresh-fixture" || token.ExpiresAt <= float64(time.Now().Unix()) || string(raw["future_field"]) != `"keep"` {
		t.Fatal("renewal damaged credential store")
	}
	info, err := os.Stat(c.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("renewed credential is not private")
	}
	if _, err := os.Stat(filepath.Join(c.Home, "oauth", c.Name+".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refresh lock was not released")
	}
}

func TestLocalOAuthFailureKeepsCredentialsAndRedactsResponse(t *testing.T) {
	for _, status := range []int{400, 401, 500, 200} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := localOAuthFixture(t, true)
			before, _ := os.ReadFile(c.Path())
			c.client = &http.Client{Transport: oauthTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":"secret-response-fixture","access_token":"incomplete-fixture"}`))}, nil
			})}
			_, err := c.Token(t.Context())
			if err == nil || strings.Contains(err.Error(), "fixture") {
				t.Fatal("invalid or unredacted renewal error")
			}
			after, _ := os.ReadFile(c.Path())
			if string(before) != string(after) {
				t.Fatal("failed refresh replaced working credential metadata")
			}
		})
	}
}

func TestLocalOAuthWaitsForExistingCLILock(t *testing.T) {
	c := localOAuthFixture(t, true)
	path := filepath.Join(c.Home, "oauth", c.Name+".lock")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.Token(ctx); err == nil {
		t.Fatal("renewal ignored another CLI process")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("another process's lock was removed")
	}
}
