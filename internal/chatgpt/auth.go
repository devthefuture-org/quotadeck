package chatgpt

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
)

const (
	issuer        = "https://auth.openai.com"
	resource      = "https://api.openai.com/v1"
	planScope     = "chatgpt.tokens.use.direct"
	dynamicClient = "dynamic_agent_client"
	loginTimeout  = 10 * time.Minute
)

var (
	ErrNotConnected = errors.New("connect ChatGPT and allow QuotaDeck to use your plan")
	errPlanScope    = errors.New("OpenAI did not grant access to your ChatGPT plan")
)

type tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshAfter int64  `json:"earliest_refresh_at"`
}

type credential struct {
	tokens
	ClientID string    `json:"client_id"`
	Subject  string    `json:"subject"`
	Email    string    `json:"email,omitempty"`
	Expires  time.Time `json:"expires_at"`
}

type Status struct {
	Connected     bool   `json:"connected"`
	Pending       bool   `json:"pending"`
	Email         string `json:"email,omitempty"`
	Error         string `json:"error,omitempty"`
	Completed     uint64 `json:"completed"`
	LastRequestID string `json:"lastRequestId,omitempty"`
}

type login struct {
	server    *http.Server
	cancel    context.CancelFunc
	finishing bool
}

type Manager struct {
	dir      string
	authURL  string
	apiURL   string
	authHTTP *http.Client
	apiHTTP  *http.Client

	mu          sync.Mutex
	pending     *login
	loginError  string
	completed   uint64
	lastRequest string
	closed      bool
}

func New(database string) *Manager {
	return &Manager{
		dir: stateDir(database), authURL: issuer, apiURL: resource,
		authHTTP: &http.Client{Timeout: 25 * time.Second, CheckRedirect: rejectRedirect},
		apiHTTP:  &http.Client{Timeout: 10 * time.Minute, CheckRedirect: rejectRedirect},
	}
}

func rejectRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func stateDir(database string) string { return database + ".chatgpt" }

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := Status{Pending: m.pending != nil, Error: m.loginError, Completed: m.completed, LastRequestID: m.lastRequest}
	if saved, err := m.load(); err == nil {
		result.Connected = hasPlanScope(saved.Scope)
		result.Email = saved.Email
	}
	return result
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.cancelLogin()
}

func (m *Manager) CancelLogin() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelLogin()
}

func (m *Manager) cancelLogin() {
	if m.pending != nil {
		m.pending.cancel()
		_ = m.pending.server.Close()
		m.pending = nil
	}
}

func (m *Manager) Start() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", errors.New("QuotaDeck is shutting down")
	}
	if m.pending != nil {
		if m.pending.finishing {
			return "", errors.New("this sign-in is completing")
		}
		m.cancelLogin()
	}
	host, err := m.hostID()
	if err != nil {
		return "", err
	}
	if err = m.ensureGatewayKey(); err != nil {
		return "", err
	}
	saved, err := m.load()
	if err != nil && !errors.Is(err, ErrNotConnected) {
		return "", err
	}
	state, err := randomString()
	if err != nil {
		return "", err
	}
	nonce, err := randomString()
	if err != nil {
		return "", err
	}
	verifier, err := randomString()
	if err != nil {
		return "", err
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	redirect := "http://" + listener.Addr().String() + "/auth/callback"
	challenge := sha256.Sum256([]byte(verifier))
	params := url.Values{
		"client_id": {dynamicClient}, "agent_name_hint": {"QuotaDeck"}, "ext_agent_host_id": {host},
		"response_type": {"code"}, "redirect_uri": {redirect}, "resource": {resource},
		"scope": {"openid profile email offline_access resource.invoke " + planScope},
		"state": {state}, "nonce": {nonce}, "code_challenge_method": {"S256"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}
	if saved.ClientID != "" {
		params.Set("client_id", saved.ClientID)
		params.Del("agent_name_hint")
		if saved.Email != "" {
			params.Set("login_hint", saved.Email)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	mux := http.NewServeMux()
	attempt := &login{server: &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}, cancel: cancel}
	m.pending = attempt
	m.loginError = ""
	mux.HandleFunc("GET /auth/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		query := r.URL.Query()
		if r.Host != listener.Addr().String() || subtle.ConstantTimeCompare([]byte(state), []byte(query.Get("state"))) != 1 {
			http.Error(w, "Invalid QuotaDeck sign-in callback.", http.StatusBadRequest)
			return
		}
		m.mu.Lock()
		if m.pending != attempt || attempt.finishing {
			m.mu.Unlock()
			http.Error(w, "This QuotaDeck sign-in is no longer pending.", http.StatusConflict)
			return
		}
		attempt.finishing = true
		m.mu.Unlock()
		result, err := m.completeLogin(ctx, query, saved, redirect, verifier, nonce)
		if err = m.finishLogin(attempt, result, err); err != nil {
			http.Error(w, "ChatGPT could not connect to QuotaDeck. Return to the dashboard for details.", http.StatusBadRequest)
		} else {
			_, _ = io.WriteString(w, "ChatGPT plan connected. You can return to QuotaDeck.")
		}
	})
	go func() {
		<-ctx.Done()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			m.finishLogin(attempt, credential{}, errors.New("ChatGPT sign-in timed out"))
		}
		shutdown, done := context.WithTimeout(context.Background(), 4*time.Second)
		defer done()
		_ = attempt.server.Shutdown(shutdown)
	}()
	go func() {
		if err := attempt.server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			m.finishLogin(attempt, credential{}, errors.New("the ChatGPT callback listener stopped"))
		}
	}()
	return m.authURL + "/api/accounts/authorize?" + params.Encode(), nil
}

func (m *Manager) completeLogin(ctx context.Context, params url.Values, previous credential, redirect, verifier, nonce string) (credential, error) {
	if params.Get("error") != "" {
		return credential{}, errors.New("OpenAI did not authorize ChatGPT sign-in")
	}
	id := params.Get("client_id")
	if previous.ClientID != "" {
		if id != "" && id != previous.ClientID {
			return credential{}, errors.New("OpenAI returned a different ChatGPT registration")
		}
		id = previous.ClientID
	}
	if id == "" || id == dynamicClient || params.Get("code") == "" {
		return credential{}, errors.New("OpenAI did not complete ChatGPT registration")
	}
	exchanged, err := m.exchange(ctx, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {id}, "code": {params.Get("code")},
		"code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {resource},
	})
	if err != nil {
		return credential{}, err
	}
	if !hasPlanScope(exchanged.Scope) {
		return credential{}, errPlanScope
	}
	verifierContext := oidc.ClientContext(context.Background(), m.authHTTP)
	keySet := oidc.NewRemoteKeySet(verifierContext, m.authURL+"/.well-known/jwks.json")
	identity, err := oidc.NewVerifier(m.authURL, keySet, &oidc.Config{ClientID: id}).Verify(ctx, exchanged.IDToken)
	if err != nil || identity.Subject == "" || identity.Nonce != nonce {
		return credential{}, errors.New("OpenAI identity verification failed")
	}
	if previous.Subject != "" && identity.Subject != previous.Subject {
		return credential{}, errors.New("OpenAI returned a different ChatGPT account")
	}
	var claims struct {
		Email string `json:"email"`
	}
	if err := identity.Claims(&claims); err != nil {
		return credential{}, errors.New("OpenAI identity claims could not be read")
	}
	result := credential{tokens: exchanged, ClientID: id, Subject: identity.Subject, Email: claims.Email}
	if err := result.setExpiry(); err != nil {
		return credential{}, err
	}
	return result, nil
}

func (m *Manager) finishLogin(attempt *login, result credential, loginErr error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != attempt {
		return errors.New("this ChatGPT sign-in expired")
	}
	if loginErr == nil {
		if err := writePrivate(filepath.Join(m.dir, "credentials.json"), result); err != nil {
			loginErr = errors.New("could not save ChatGPT credentials locally")
		}
	}
	if loginErr != nil {
		m.loginError = loginErr.Error()
	}
	m.pending = nil
	attempt.cancel()
	return loginErr
}

func (m *Manager) hostID() (string, error) {
	path := filepath.Join(m.dir, "host.json")
	var id string
	err := readPrivate(path, &id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	value, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	id = "urn:uuid:" + value.String()
	return id, writePrivate(path, id)
}

func (m *Manager) ensureGatewayKey() error {
	if _, err := gatewayKey(m.dir); !errors.Is(err, os.ErrNotExist) {
		return err
	}
	key, err := randomString()
	if err != nil {
		return err
	}
	return writePrivate(filepath.Join(m.dir, "gateway.json"), key)
}

func GatewayKey(database string) (string, error) { return gatewayKey(stateDir(database)) }

func gatewayKey(dir string) (string, error) {
	var key string
	err := readPrivate(filepath.Join(dir, "gateway.json"), &key)
	if err == nil && len(key) != 43 {
		err = errors.New("invalid local ChatGPT gateway key")
	}
	return key, err
}

func (m *Manager) load() (credential, error) {
	var saved credential
	err := readPrivate(filepath.Join(m.dir, "credentials.json"), &saved)
	if errors.Is(err, os.ErrNotExist) {
		return credential{}, ErrNotConnected
	}
	return saved, err
}

func (m *Manager) Disconnect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelLogin()
	m.loginError = ""
	saved, err := m.load()
	if errors.Is(err, ErrNotConnected) {
		return nil
	}
	if err != nil {
		return err
	}
	form := url.Values{
		"token": {saved.RefreshToken}, "token_type_hint": {"refresh_token"}, "client_id": {saved.ClientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.authURL+"/api/accounts/oauth/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := m.authHTTP.Do(req)
	if err != nil {
		return errors.New("could not reach the OpenAI sign-in service")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("OpenAI returned HTTP %d while disconnecting", response.StatusCode)
	}
	if err := os.Remove(filepath.Join(m.dir, "credentials.json")); err != nil {
		return err
	}
	m.completed = 0
	m.lastRequest = ""
	return nil
}

func (m *Manager) accessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	saved, err := m.load()
	if err != nil {
		return "", err
	}
	if !hasPlanScope(saved.Scope) {
		return "", errPlanScope
	}
	now := time.Now()
	if now.Before(saved.Expires) && (now.Add(time.Minute).Before(saved.Expires) || now.Unix() < saved.RefreshAfter) {
		return saved.AccessToken, nil
	}
	renewed, err := m.exchange(ctx, url.Values{
		"grant_type": {"refresh_token"}, "client_id": {saved.ClientID},
		"refresh_token": {saved.RefreshToken}, "resource": {resource},
	})
	if err != nil {
		return "", err
	}
	if renewed.Scope == "" {
		renewed.Scope = saved.Scope
	}
	// Renewal does not need to return an ID token. Keep the validated identity
	// from the original authorization and rotate the other credentials together.
	renewed.IDToken = saved.IDToken
	saved.tokens = renewed
	if err := saved.setExpiry(); err != nil {
		return "", err
	}
	if err := writePrivate(filepath.Join(m.dir, "credentials.json"), saved); err != nil {
		return "", errors.New("could not save renewed ChatGPT credentials")
	}
	if !hasPlanScope(saved.Scope) {
		return "", errPlanScope
	}
	return saved.AccessToken, nil
}

func (m *Manager) exchange(ctx context.Context, form url.Values) (tokens, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", m.authURL+"/api/accounts/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := m.authHTTP.Do(req)
	if err != nil {
		return tokens{}, errors.New("could not reach the OpenAI sign-in service")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var failure struct{ Error string }
		_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&failure)
		switch failure.Error {
		case "invalid_grant", "invalid_client", "token_expired", "refresh_token_reused":
			return tokens{}, fmt.Errorf("OpenAI requires a new ChatGPT sign-in (%s)", failure.Error)
		}
		return tokens{}, fmt.Errorf("OpenAI sign-in returned HTTP %d", response.StatusCode)
	}
	var result tokens
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return tokens{}, errors.New("OpenAI returned an invalid token response")
	}
	return result, nil
}

func (c *credential) setExpiry() error {
	if c.AccessToken == "" || c.RefreshToken == "" || !strings.EqualFold(c.TokenType, "Bearer") ||
		c.ExpiresIn <= 0 || c.ExpiresIn > int64((30*24*time.Hour)/time.Second) {
		return errors.New("OpenAI returned incomplete ChatGPT credentials")
	}
	c.Expires = time.Now().Add(time.Duration(c.ExpiresIn) * time.Second)
	return nil
}

func hasPlanScope(scope string) bool {
	for _, value := range strings.Fields(scope) {
		if value == planScope {
			return true
		}
	}
	return false
}

func randomString() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(random[:]), nil
}

func readPrivate(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("ChatGPT state requires a private regular file")
	}
	return json.NewDecoder(io.LimitReader(file, 64<<10)).Decode(target)
}

func writePrivate(path string, value any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("ChatGPT state requires a private directory")
	}
	tmp, err := os.CreateTemp(dir, ".chatgpt-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = json.NewEncoder(tmp).Encode(value)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
