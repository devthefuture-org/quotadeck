//go:build desktop

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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
