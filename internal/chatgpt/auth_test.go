package chatgpt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSignInAndRefresh(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	manager := New(filepath.Join(t.TempDir(), "state.db"))
	defer manager.Close()
	const clientID = "oaiapp_test"
	var nonce, redirect, challenge string
	var refreshed bool
	mux := http.NewServeMux()
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	manager.authURL = upstream.URL
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(object{"keys": []object{{
			"kty": "RSA", "kid": "test", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB",
		}}})
	})
	mux.HandleFunc("POST /api/accounts/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("client_id") != clientID || r.FormValue("resource") != resource {
			t.Error("incorrect token exchange client or resource")
			w.WriteHeader(400)
			return
		}
		switch r.FormValue("grant_type") {
		case "authorization_code":
			pkce := sha256.Sum256([]byte(r.FormValue("code_verifier")))
			if r.FormValue("code") != "test_code" || r.FormValue("redirect_uri") != redirect ||
				base64.RawURLEncoding.EncodeToString(pkce[:]) != challenge {
				t.Error("incorrect authorization-code exchange")
				w.WriteHeader(400)
				return
			}
			id := testIDToken(t, key, object{
				"iss": upstream.URL, "aud": clientID, "nonce": nonce, "sub": "test-id",
				"exp": time.Now().Add(time.Hour).Unix(), "email": "test@example.invalid",
			})
			_ = json.NewEncoder(w).Encode(tokens{
				AccessToken: "access", RefreshToken: "refresh", IDToken: id, TokenType: "Bearer",
				Scope: "openid " + planScope, ExpiresIn: 1,
			})
		case "refresh_token":
			if r.FormValue("refresh_token") != "refresh" {
				t.Error("incorrect refresh token")
				w.WriteHeader(400)
				return
			}
			refreshed = true
			_ = json.NewEncoder(w).Encode(tokens{AccessToken: "new_access", RefreshToken: "new_refresh", TokenType: "Bearer", ExpiresIn: 3600})
		default:
			t.Error("unexpected token exchange")
			w.WriteHeader(400)
		}
	})
	mux.HandleFunc("POST /api/accounts/oauth/revoke", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("token") != "new_refresh" || r.FormValue("client_id") != clientID {
			t.Error("incorrect revocation request")
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(200)
	})

	loginURL, err := manager.Start()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	values := parsed.Query()
	nonce, redirect, challenge = values.Get("nonce"), values.Get("redirect_uri"), values.Get("code_challenge")
	if values.Get("client_id") != dynamicClient || values.Get("agent_name_hint") != "QuotaDeck" ||
		!hasPlanScope(values.Get("scope")) || values.Get("ext_agent_host_id") == "" {
		t.Fatalf("unexpected authorization request: %v", values)
	}
	callback := func(state string, want int) {
		t.Helper()
		result, err := http.Get(redirect + "?" + (url.Values{
			"code": {"test_code"}, "state": {state}, "client_id": {clientID},
		}).Encode())
		if err != nil {
			t.Fatal(err)
		}
		_ = result.Body.Close()
		if result.StatusCode != want {
			t.Errorf("callback status %d, want %d", result.StatusCode, want)
		}
	}
	callback("wrong-state", 400)
	callback(values.Get("state"), 200)
	if status := manager.Status(); !status.Connected || status.Pending || status.Email != "test@example.invalid" {
		t.Fatalf("unexpected connection status: %+v", status)
	}
	token, err := manager.accessToken(context.Background())
	if err != nil || token != "new_access" || !refreshed {
		t.Fatal("the expired access token was not refreshed")
	}
	stored, err := manager.load()
	if err != nil || stored.RefreshToken != "new_refresh" || stored.IDToken == "" || !hasPlanScope(stored.Scope) {
		t.Fatal("renewal did not preserve the authorized identity and rotate the tokens")
	}
	info, err := os.Stat(filepath.Join(manager.dir, "credentials.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("credential permissions are not private")
	}
	returningURL, err := manager.Start()
	if err != nil {
		t.Fatal(err)
	}
	returning, err := url.Parse(returningURL)
	if err != nil {
		t.Fatal(err)
	}
	if returning.Query().Get("client_id") != clientID ||
		returning.Query().Get("ext_agent_host_id") != values.Get("ext_agent_host_id") {
		t.Fatal("reauthorization did not reuse the issued registration and host ID")
	}
	manager.CancelLogin()
	if !manager.Status().Connected {
		t.Fatal("cancelling reauthorization disconnected the active account")
	}
	if err := manager.Disconnect(context.Background()); err != nil || manager.Status().Connected {
		t.Fatalf("disconnect failed: %v", err)
	}
}

func testIDToken(t *testing.T, key *rsa.PrivateKey, claims object) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Error(err)
		return ""
	}
	raw := base64.RawURLEncoding
	body := raw.EncodeToString([]byte(`{"alg":"RS256","kid":"test","typ":"JWT"}`)) + "." + raw.EncodeToString(payload)
	digest := sha256.Sum256([]byte(body))
	signed, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Error(fmt.Errorf("sign ID token: %w", err))
		return ""
	}
	return body + "." + raw.EncodeToString(signed)
}
