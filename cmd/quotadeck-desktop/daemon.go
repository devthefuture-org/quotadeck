//go:build desktop

package main

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/config"
)

const daemonStartupTimeout = 4 * time.Second

// attachDaemon makes the desktop window a thin client of the packaged user
// service. Keeping one poller process avoids contradictory snapshots when a
// desktop launcher and systemd expose different provider environments.
func attachDaemon(cfg config.Config) (http.Handler, bool) {
	baseURL := daemonBaseURL(cfg)
	if daemonHealthy(baseURL, 500*time.Millisecond) {
		return newDaemonProxy(baseURL), true
	}
	// An AppImage is intentionally self-contained. Do not attach it to a
	// potentially unrelated distro package installed on the same machine.
	if strings.TrimSpace(os.Getenv("APPIMAGE")) != "" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), daemonStartupTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, "systemctl", "--user", "start", "quotadeck.service").Run(); err != nil {
		return nil, false
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if daemonHealthy(baseURL, 400*time.Millisecond) {
			return newDaemonProxy(baseURL), true
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-ticker.C:
		}
	}
}

func daemonBaseURL(cfg config.Config) string {
	return "http://" + net.JoinHostPort(cfg.Server.Bind, strconv.Itoa(cfg.Server.Port))
}

func daemonHealthy(baseURL string, timeout time.Duration) bool {
	client := &http.Client{Timeout: timeout}
	response, err := client.Get(strings.TrimRight(baseURL, "/") + "/api/v1/health")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode >= 200 && response.StatusCode < 300
}

func newDaemonProxy(baseURL string) *httputil.ReverseProxy {
	target, err := url.Parse(baseURL)
	if err != nil {
		panic(err)
	}
	return &httputil.ReverseProxy{
		Rewrite: func(request *httputil.ProxyRequest) {
			request.SetURL(target)
			request.Out.Host = target.Host
			if request.In.Header.Get("Origin") != "" {
				request.Out.Header.Set("Origin", target.Scheme+"://"+target.Host)
			}
		},
		ErrorHandler: func(writer http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(writer, "QuotaDeck local service is unavailable", http.StatusServiceUnavailable)
		},
	}
}

// loadManagedEnvironment implements the small NAME=value subset emitted by
// QuotaDeck's plan controls. Existing process values win so explicit shell or
// launcher configuration retains precedence. Values are never logged.
func loadManagedEnvironment(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || !validEnvironmentName(name) || os.Getenv(name) != "" {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || character == '_' || (index > 0 && character >= '0' && character <= '9') {
			continue
		}
		return false
	}
	return true
}
