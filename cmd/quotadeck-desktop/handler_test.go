//go:build desktop

package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestTrustedDesktopHandlerNormalisesWailsRequest(t *testing.T) {
	var remote, origin string
	next := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		remote = request.RemoteAddr
		origin = request.Header.Get("Origin")
	})
	request := httptest.NewRequest(http.MethodPost, "http://wails.localhost/api/v1/refresh", nil)
	request.Header.Set("Origin", "wails://wails")
	trustedDesktopHandler(next).ServeHTTP(httptest.NewRecorder(), request)
	if remote != "127.0.0.1:0" {
		t.Fatalf("remote address = %q", remote)
	}
	if origin != "" {
		t.Fatalf("origin = %q", origin)
	}
}

func TestDaemonProxyForwardsRequestsAndNormalisesOrigin(t *testing.T) {
	var host, origin, path string
	proxy := newDaemonProxy("http://127.0.0.1:9211")
	proxy.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		host, origin, path = request.Host, request.Header.Get("Origin"), request.URL.Path
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
		}, nil
	})

	request := httptest.NewRequest(http.MethodGet, "http://wails.localhost/api/v1/health", nil)
	request.Header.Set("Origin", "wails://wails")
	recorder := httptest.NewRecorder()
	proxy.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || path != "/api/v1/health" {
		t.Fatalf("proxy returned %d for path %q", recorder.Code, path)
	}
	if host != "127.0.0.1:9211" || origin != "http://127.0.0.1:9211" {
		t.Fatalf("proxy host/origin = %q / %q", host, origin)
	}
}

func TestLoadManagedEnvironmentUsesMissingValuesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment")
	if err := os.WriteFile(path, []byte("ZAI_API_KEY=managed-secret\nGLM_API_KEY=managed-glm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZAI_API_KEY", "explicit-secret")
	t.Setenv("GLM_API_KEY", "")
	if err := loadManagedEnvironment(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ZAI_API_KEY"); got != "explicit-secret" {
		t.Fatal("explicit environment value was replaced")
	}
	if got := os.Getenv("GLM_API_KEY"); got != "managed-glm" {
		t.Fatalf("managed environment value = %q", got)
	}
}
