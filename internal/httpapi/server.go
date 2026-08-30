package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/devthefuture-org/quotadeck/internal/doctor"
	"github.com/devthefuture-org/quotadeck/internal/poller"
)

//go:embed ui
var uiFiles embed.FS

type Server struct {
	engine    *poller.Engine
	doctor    doctor.Collector
	startedAt time.Time
	handler   http.Handler
}

func New(engine *poller.Engine, collector doctor.Collector) *Server {
	server := &Server{engine: engine, doctor: collector, startedAt: time.Now().UTC()}
	server.handler = server.routes()
	return server
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/state", s.state)
	mux.HandleFunc("GET /api/v1/providers", s.providers)
	mux.HandleFunc("GET /api/v1/accounts", s.accounts)
	mux.HandleFunc("GET /api/v1/accounts/{id}/history", s.history)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/doctor", s.doctorReport)
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("POST /api/v1/refresh", s.refresh)
	mux.Handle("/", spaHandler())
	return securityHeaders(mux)
}

func (s *Server) state(writer http.ResponseWriter, request *http.Request) {
	states, err := s.engine.Current(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "state_unavailable", "state is temporarily unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"generatedAt": time.Now().UTC(), "providers": s.engine.Providers(), "accounts": states,
	})
}

func (s *Server) providers(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, s.engine.Providers())
}

func (s *Server) accounts(writer http.ResponseWriter, request *http.Request) {
	states, err := s.engine.Current(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "accounts_unavailable", "accounts are temporarily unavailable")
		return
	}
	accounts := make([]any, 0, len(states))
	for _, state := range states {
		accounts = append(accounts, state.Account)
	}
	writeJSON(writer, http.StatusOK, accounts)
}

func (s *Server) history(writer http.ResponseWriter, request *http.Request) {
	accountID := request.PathValue("id")
	from, err := optionalTime(request.URL.Query().Get("from"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_from", "from must be an RFC3339 timestamp")
		return
	}
	to, err := optionalTime(request.URL.Query().Get("to"))
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_to", "to must be an RFC3339 timestamp")
		return
	}
	history, err := s.engine.History(request.Context(), accountID, from, to)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "history_unavailable", "history is temporarily unavailable")
		return
	}
	writeJSON(writer, http.StatusOK, history)
}

func (s *Server) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": "ok", "uptimeSeconds": int64(time.Since(s.startedAt).Seconds()),
		"refreshing": s.engine.Refreshing(), "time": time.Now().UTC(),
	})
}

func (s *Server) doctorReport(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, s.doctor.Collect(request.Context()))
}

func (s *Server) events(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, "streaming_unavailable", "streaming is unavailable")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache, no-store")
	writer.Header().Set("Connection", "keep-alive")
	writer.Header().Set("X-Accel-Buffering", "no")
	channel, unsubscribe := s.engine.Hub().Subscribe()
	defer unsubscribe()
	_, _ = fmt.Fprintf(writer, "event: ready\ndata: {\"type\":\"ready\"}\n\n")
	flusher.Flush()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case payload, ok := <-channel:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(writer, "event: update\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = fmt.Fprint(writer, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) refresh(writer http.ResponseWriter, request *http.Request) {
	if !isLoopbackRequest(request) {
		writeError(writer, http.StatusForbidden, "local_only", "refresh is available from loopback only")
		return
	}
	if request.Header.Get("X-QuotaDeck-Request") != "refresh" {
		writeError(writer, http.StatusForbidden, "csrf_guard", "missing refresh request header")
		return
	}
	err := s.engine.Refresh(request.Context())
	if errors.Is(err, poller.ErrRefreshInProgress) {
		writeError(writer, http.StatusConflict, "refresh_in_progress", "a refresh is already in progress")
		return
	}
	if err != nil {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "completed_with_errors", "message": "some providers are unavailable"})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"status": "completed"})
}

func spaHandler() http.Handler {
	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, "/api/") {
			writeError(writer, http.StatusNotFound, "not_found", "route not found")
			return
		}
		path := strings.TrimPrefix(request.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			clone := request.Clone(request.Context())
			clone.URL.Path = "/"
			fileServer.ServeHTTP(writer, clone)
			return
		}
		if strings.Contains(path, "/assets/") || strings.HasPrefix(path, "assets/") {
			writer.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(writer, request)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}

func isLoopbackRequest(request *http.Request) bool {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		host = request.RemoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	origin := request.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	originHost := parsed.Hostname()
	return originHost == "localhost" || (net.ParseIP(originHost) != nil && net.ParseIP(originHost).IsLoopback())
}

func optionalTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, raw)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func Shutdown(ctx context.Context, server *http.Server) error {
	return server.Shutdown(ctx)
}
