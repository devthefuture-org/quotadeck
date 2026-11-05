package kimi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

var ErrLoginRequired = errors.New("Kimi Code login needs renewal; run kimi login")

// LocalCredential contains locations only. Tokens never enter domain/API state.
type LocalCredential struct {
	Home, Name, BaseURL, OAuthHost string
	client                         *http.Client
}

// LocalCredentials follows the CLI's configured OAuth reference instead of
// guessing a credential filename or sending a global token to mainland Kimi.
func LocalCredentials(homes []string) []LocalCredential {
	if len(homes) == 0 {
		homes = []string{"~/.kimi-code"}
	}
	var result []LocalCredential
	for _, rawHome := range homes {
		home, err := filepath.Abs(config.ExpandPath(rawHome))
		if err != nil {
			continue
		}
		body, err := os.ReadFile(filepath.Join(home, "config.toml"))
		if err != nil {
			continue
		}
		var cfg struct {
			DefaultModel string `toml:"default_model"`
			Models       map[string]struct {
				Provider string `toml:"provider"`
			} `toml:"models"`
			Providers map[string]struct {
				BaseURL string `toml:"base_url"`
				OAuth   struct {
					Storage string `toml:"storage"`
					Key     string `toml:"key"`
					Host    string `toml:"oauth_host"`
				} `toml:"oauth"`
			} `toml:"providers"`
		}
		if toml.Unmarshal(body, &cfg) != nil {
			continue
		}
		names := make([]string, 0, len(cfg.Providers))
		for name := range cfg.Providers {
			names = append(names, name)
		}
		defaultProvider := cfg.Models[cfg.DefaultModel].Provider
		sort.Slice(names, func(i, j int) bool {
			if (names[i] == defaultProvider) != (names[j] == defaultProvider) {
				return names[i] == defaultProvider
			}
			return names[i] < names[j]
		})
		for _, name := range names {
			provider := cfg.Providers[name]
			key, ok := strings.CutPrefix(provider.OAuth.Key, "oauth/")
			if !ok || provider.OAuth.Storage != "file" || !safeCredentialName(key) {
				continue
			}
			base := strings.TrimRight(provider.BaseURL, "/")
			host := strings.TrimRight(provider.OAuth.Host, "/")
			if host == "" {
				host = "https://auth.kimi.com"
			}
			// OAuth credentials may only go to their configured official region.
			if !(host == "https://auth.kimi.com" && base == "https://api.kimi.com/coding/v1" ||
				host == "https://auth.kimi.ai" && base == "https://api.kimi.ai/coding/v1") {
				continue
			}
			credential := LocalCredential{Home: home, Name: key, BaseURL: base, OAuthHost: host}
			token, _, err := credential.read()
			if err == nil && token.AccessToken != "" && (token.RefreshToken != "" || token.ExpiresAt > float64(time.Now().Unix())) {
				result = append(result, credential)
			}
		}
	}
	return result
}

func safeCredentialName(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	for _, char := range name {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-_.", char)) {
			return false
		}
	}
	return true
}

func (c LocalCredential) Path() string     { return filepath.Join(c.Home, "credentials", c.Name+".json") }
func (c LocalCredential) Endpoint() string { return strings.TrimSuffix(c.BaseURL, "v1") }

type oauthToken struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresAt    float64 `json:"expires_at"`
	ExpiresIn    float64 `json:"expires_in"`
}

func (c LocalCredential) read() (oauthToken, map[string]json.RawMessage, error) {
	body, err := os.ReadFile(c.Path())
	var token oauthToken
	var raw map[string]json.RawMessage
	if err != nil || json.Unmarshal(body, &token) != nil || json.Unmarshal(body, &raw) != nil || token.AccessToken == "" {
		return token, nil, ErrLoginRequired
	}
	return token, raw, nil
}

// Token re-reads on every call and renews short-lived credentials in the CLI's
// own store. The refresh protocol and lock match @moonshot-ai/kimi-code-oauth.
func (c LocalCredential) Token(ctx context.Context) (string, error) {
	if !safeCredentialName(c.Name) || !(c.OAuthHost == "https://auth.kimi.com" && c.BaseURL == "https://api.kimi.com/coding/v1" || c.OAuthHost == "https://auth.kimi.ai" && c.BaseURL == "https://api.kimi.ai/coding/v1") {
		return "", errors.New("unrecognized Kimi OAuth configuration")
	}
	token, _, err := c.read()
	if err != nil {
		return "", err
	}
	if token.ExpiresAt > float64(time.Now().Add(90*time.Second).Unix()) {
		return token.AccessToken, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	release, err := c.lock(ctx, cancel)
	if err != nil {
		return "", err
	}
	defer release()
	token, raw, err := c.read()
	if err != nil {
		return "", err
	}
	if token.ExpiresAt > float64(time.Now().Add(90*time.Second).Unix()) {
		return token.AccessToken, nil
	}
	if token.RefreshToken == "" {
		return "", ErrLoginRequired
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {"17e5f671-d194-4dfb-9706-5516cb48c098"}, "refresh_token": {token.RefreshToken}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.OAuthHost+"/api/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("invalid Kimi OAuth endpoint")
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "QuotaDeck")
	if device, err := os.ReadFile(filepath.Join(c.Home, "device_id")); err == nil {
		value := strings.TrimSpace(string(device))
		if !strings.ContainsAny(value, "\r\n") {
			request.Header.Set("X-Msh-Device-Id", value)
		}
	}
	client := c.client
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("Kimi login renewal could not reach the authentication server")
	}
	defer response.Body.Close()
	if response.StatusCode == 401 || response.StatusCode == 403 || response.StatusCode == 400 {
		return "", ErrLoginRequired
	}
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Kimi login renewal returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	var next oauthToken
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(body, &next) != nil || json.Unmarshal(body, &fields) != nil || next.AccessToken == "" || next.RefreshToken == "" || next.ExpiresIn <= 0 {
		return "", errors.New("Kimi login renewal returned an invalid credential")
	}
	for _, name := range []string{"access_token", "refresh_token", "expires_in", "token_type", "scope"} {
		if value, ok := fields[name]; ok {
			raw[name] = value
		}
	}
	raw["expires_at"], _ = json.Marshal(float64(time.Now().Unix()) + next.ExpiresIn)
	if err := ctx.Err(); err != nil {
		return "", errors.New("Kimi login renewal lost its lock or timed out")
	}
	encoded, _ := json.MarshalIndent(raw, "", "  ")
	if err := writeOAuthFile(c.Path(), append(encoded, '\n')); err != nil {
		return "", errors.New("Kimi login renewed but could not be saved")
	}
	return next.AccessToken, nil
}

func writeOAuthFile(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".quotadeck-oauth-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err = tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err = tmp.Write(body); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// proper-lockfile uses an atomic mkdir and a refreshed mtime, not flock.
// Keep its 5-second stale threshold satisfied while a renewal is in flight.
func (c LocalCredential) lock(ctx context.Context, cancel context.CancelFunc) (func(), error) {
	dir := filepath.Join(c.Home, "oauth")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errors.New("cannot prepare Kimi login lock")
	}
	path := filepath.Join(dir, c.Name+".lock")
	for {
		if err := os.Mkdir(path, 0o700); err == nil {
			break
		} else if !errors.Is(err, os.ErrExist) {
			return nil, errors.New("cannot acquire Kimi login lock")
		}
		if info, err := os.Stat(path); err == nil && info.IsDir() && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(path) // empty lock directories only; never remove contents
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("Kimi login is being renewed by another process; retry shortly")
		case <-time.After(100 * time.Millisecond):
		}
	}
	owner, err := os.Stat(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, errors.New("cannot inspect Kimi login lock")
	}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case now := <-ticker.C:
				current, err := os.Stat(path)
				if err != nil || !os.SameFile(owner, current) {
					cancel()
					return
				}
				if err := os.Chtimes(path, now, now); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
		if current, err := os.Stat(path); err == nil && os.SameFile(owner, current) {
			_ = os.Remove(path)
		}
	}, nil
}
