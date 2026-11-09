package opencode

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
	_ "modernc.org/sqlite"
)

const Endpoint = "https://opencode.ai/inference/go/anthropic"
const Model = "deepseek-v4.1-flash"
const consoleURL = "https://opencode.ai/console"

var ErrLoginRequired = errors.New("reconnect OpenCode Console in OpenCode v2")

// Credentials retain locations and public identity only. Tokens are reread for
// each request; they never enter QuotaDeck's snapshots or HTTP responses.
type Credential struct {
	Database, State, ID, Label, OrgID string
	Active                            bool
	client                            *http.Client
}

type value struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Method   string `json:"methodID"`
	Access   string `json:"access"`
	Refresh  string `json:"refresh"`
	Expires  int64  `json:"expires"`
	Metadata struct {
		Server string `json:"server"`
		OrgID  string `json:"orgID"`
	} `json:"metadata"`
}

func Paths(cfg config.OpenCodeConfig) (string, string) {
	home, _ := os.UserHomeDir()
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(home, ".local", "share")
	}
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	database := cfg.DatabasePath
	if database == "" {
		database = filepath.Join(data, "opencode", "opencode.db")
	}
	if cfg.StatePath != "" {
		state = config.ExpandPath(cfg.StatePath)
	} else {
		state = filepath.Join(state, "opencode")
	}
	database, _ = filepath.Abs(config.ExpandPath(database))
	state, _ = filepath.Abs(state)
	return database, state
}

func openDatabase(path, mode string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{"mode": {mode}, "_pragma": {"busy_timeout(3000)"}}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err == nil {
		db.SetMaxOpenConns(1)
	}
	return db, err
}

func valid(v value) bool {
	server := strings.TrimRight(v.Metadata.Server, "/")
	if server != "" && server != consoleURL && server != "https://console.opencode.ai" {
		return false
	}
	if strings.ContainsAny(v.Metadata.OrgID, "\r\n") {
		return false
	}
	return v.Type == "key" && v.Key != "" || v.Type == "oauth" && v.Method == "device" && v.Access != "" && v.Refresh != "" && v.Metadata.OrgID != ""
}

func LocalCredentials(ctx context.Context, cfg config.OpenCodeConfig) ([]Credential, error) {
	path, state := Paths(cfg)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	db, err := openDatabase(path, "ro")
	if err != nil {
		return nil, errors.New("OpenCode credential database is unavailable")
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT id,label,value,COALESCE(active,0) FROM credential WHERE integration_id='opencode' ORDER BY COALESCE(active,0) DESC,time_created DESC,id DESC`)
	if err != nil {
		return nil, errors.New("OpenCode v2 credential database is unavailable")
	}
	defer rows.Close()
	var result []Credential
	for rows.Next() {
		c := Credential{Database: path, State: state}
		var raw string
		if err := rows.Scan(&c.ID, &c.Label, &raw, &c.Active); err != nil {
			return nil, errors.New("OpenCode credential cannot be read")
		}
		var v value
		if json.Unmarshal([]byte(raw), &v) != nil || !valid(v) {
			continue
		}
		c.OrgID = v.Metadata.OrgID
		result = append(result, c)
	}
	return result, rows.Err()
}

func (c Credential) read(ctx context.Context) (value, string, error) {
	db, err := openDatabase(c.Database, "ro")
	if err != nil {
		return value{}, "", ErrLoginRequired
	}
	defer db.Close()
	var raw string
	if db.QueryRowContext(ctx, `SELECT value FROM credential WHERE id=? AND integration_id='opencode'`, c.ID).Scan(&raw) != nil {
		return value{}, "", ErrLoginRequired
	}
	var v value
	if json.Unmarshal([]byte(raw), &v) != nil || !valid(v) || v.Metadata.OrgID != c.OrgID {
		return value{}, "", ErrLoginRequired
	}
	return v, raw, nil
}

func secureClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (c Credential) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	return secureClient()
}

func (c Credential) Token(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	v, _, err := c.read(ctx)
	if err != nil {
		return "", err
	}
	if v.Type == "key" {
		return v.Key, nil
	}
	if v.Expires > time.Now().Add(5*time.Minute).UnixMilli() {
		return v.Access, nil
	}
	release, err := c.lock(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	v, raw, err := c.read(ctx)
	if err != nil {
		return "", err
	}
	if v.Type == "key" {
		return v.Key, nil
	}
	if v.Expires > time.Now().Add(5*time.Minute).UnixMilli() {
		return v.Access, nil
	}
	body, _ := json.Marshal(map[string]string{"grant_type": "refresh_token", "refresh_token": v.Refresh, "client_id": "opencode-cli"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, consoleURL+"/auth/device/token", bytes.NewReader(body))
	if err != nil {
		return "", errors.New("OpenCode refresh request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "QuotaDeck")
	response, err := c.httpClient().Do(req)
	if err != nil {
		return "", errors.New("OpenCode session refresh is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == 400 || response.StatusCode == 401 || response.StatusCode == 403 {
		return "", ErrLoginRequired
	}
	if response.StatusCode != 200 {
		return "", errors.New("OpenCode session refresh is unavailable")
	}
	var token struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int64  `json:"expires_in"`
		OrgID   string `json:"org_id"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&token) != nil || token.Access == "" || token.Refresh == "" || token.Expires <= 0 || token.Expires > 366*24*3600 || token.OrgID != "" && token.OrgID != c.OrgID {
		return "", errors.New("invalid OpenCode session refresh response")
	}
	var document map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &document) != nil {
		return "", ErrLoginRequired
	}
	document["access"], _ = json.Marshal(token.Access)
	document["refresh"], _ = json.Marshal(token.Refresh)
	document["expires"], _ = json.Marshal(time.Now().Add(time.Duration(token.Expires) * time.Second).UnixMilli())
	updated, _ := json.Marshal(document)
	db, err := openDatabase(c.Database, "rw")
	if err != nil {
		return "", errors.New("cannot save renewed OpenCode session")
	}
	defer db.Close()
	// Compare-and-swap also protects a logout, replacement or workspace change
	// performed by OpenCode while the refresh request was in flight.
	result, err := db.ExecContext(ctx, `UPDATE credential SET value=?,time_updated=? WHERE id=? AND integration_id='opencode' AND value=?`, string(updated), time.Now().UnixMilli(), c.ID, raw)
	if err != nil {
		return "", errors.New("cannot save renewed OpenCode session")
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return "", ErrLoginRequired
	}
	return token.Access, nil
}

func (c Credential) AccountID() string {
	sum := sha256.Sum256([]byte(c.Database + ":" + c.ID))
	return "opencode:" + hex.EncodeToString(sum[:8])
}

// OpenCode's EffectFlock uses SHA-1(key).lock directories in its state/locks
// directory, with a heartbeat file and a 60s stale threshold. Use its same lock
// so the CLI and QuotaDeck cannot rotate a refresh token concurrently.
func (c Credential) lock(ctx context.Context) (func(), error) {
	sum := sha1.Sum([]byte("credential-refresh:" + c.ID))
	path := filepath.Join(c.State, "locks", hex.EncodeToString(sum[:])+".lock")
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return nil, errors.New("cannot lock OpenCode session")
	}
	for {
		if err := os.Mkdir(path, 0o700); err == nil {
			info, _ := os.Stat(path)
			heartbeat := filepath.Join(path, "heartbeat")
			if os.WriteFile(heartbeat, nil, 0o600) != nil {
				os.RemoveAll(path)
				return nil, errors.New("cannot lock OpenCode session")
			}
			hostname, _ := os.Hostname()
			meta, _ := json.Marshal(map[string]any{"token": fmt.Sprintf("quotadeck-%d-%d", os.Getpid(), time.Now().UnixNano()), "pid": os.Getpid(), "hostname": hostname, "createdAt": time.Now().UTC()})
			if os.WriteFile(filepath.Join(path, "meta.json"), meta, 0o600) != nil {
				os.RemoveAll(path)
				return nil, errors.New("cannot lock OpenCode session")
			}
			stop, done := make(chan struct{}), make(chan struct{})
			go func() {
				defer close(done)
				ticker := time.NewTicker(10 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						return
					case now := <-ticker.C:
						if current, err := os.Stat(path); err == nil && os.SameFile(info, current) {
							_ = os.Chtimes(heartbeat, now, now)
						} else {
							return
						}
					}
				}
			}()
			return func() {
				close(stop)
				<-done
				if current, err := os.Stat(path); err == nil && os.SameFile(info, current) {
					_ = os.RemoveAll(path)
				}
			}, nil
		} else if !errors.Is(err, os.ErrExist) {
			return nil, errors.New("cannot lock OpenCode session")
		}
		if staleLock(path) {
			breaker := path + ".breaker"
			if os.Mkdir(breaker, 0o700) == nil {
				if staleLock(path) {
					_ = os.RemoveAll(path)
				}
				_ = os.Remove(breaker)
				continue
			}
			if stat, err := os.Stat(breaker); err == nil && time.Since(stat.ModTime()) > time.Minute {
				_ = os.Remove(breaker)
			}
		}
		select {
		case <-ctx.Done():
			return nil, errors.New("OpenCode session is busy; retry shortly")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
func staleLock(path string) bool {
	for _, item := range []string{filepath.Join(path, "heartbeat"), filepath.Join(path, "meta.json"), path} {
		if info, err := os.Stat(item); err == nil {
			return time.Since(info.ModTime()) > time.Minute
		}
	}
	return false
}
