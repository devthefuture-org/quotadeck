package chatgpt

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const GatewayPath = "/chatgpt"

type Model struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"display_name"`
	Visibility    string `json:"visibility"`
	ContextWindow int    `json:"context_window,omitempty"`
}

func (m *Manager) Models(ctx context.Context) ([]Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	token, err := m.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	response, err := m.request(ctx, http.MethodGet, "/models", token, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, readFailure(response)
	}
	var catalog struct {
		Models []Model `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&catalog); err != nil {
		return nil, errors.New("OpenAI returned an invalid model catalog")
	}
	models := make([]Model, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if model.Visibility == "list" && model.Slug != "" {
			models = append(models, model)
		}
	}
	return models, nil
}

func (m *Manager) Messages(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !m.gatewayAuthorized(r) {
		writeGatewayError(w, &inferenceError{401, "authentication_error", "invalid local QuotaDeck gateway credentials"})
		return
	}
	var req messagesRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20))
	var extra any
	if decoder.Decode(&req) != nil || !errors.Is(decoder.Decode(&extra), io.EOF) {
		writeGatewayError(w, &inferenceError{400, "invalid_request_error", "invalid Anthropic Messages request"})
		return
	}
	data, err := req.convert()
	if err != nil {
		writeGatewayError(w, &inferenceError{400, "invalid_request_error", err.Error()})
		return
	}
	payload, err := json.Marshal(data)
	if err != nil {
		writeGatewayError(w, &inferenceError{400, "invalid_request_error", "invalid tool or model input"})
		return
	}
	token, err := m.accessToken(r.Context())
	if err != nil {
		writeGatewayError(w, &inferenceError{401, "authentication_error", err.Error()})
		return
	}
	response, err := m.request(r.Context(), http.MethodPost, "/responses", token, bytes.NewReader(payload))
	if err != nil {
		failure := &inferenceError{502, "api_error", "QuotaDeck could not reach OpenAI or receive its response headers"}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			failure = &inferenceError{504, "api_error", "QuotaDeck timed out waiting for OpenAI response headers"}
		}
		writeGatewayError(w, failure)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeGatewayError(w, readFailure(response))
		return
	}
	requestID := response.Header.Get("X-Request-Id")
	if strings.HasPrefix(requestID, "req_") && len(requestID) < 128 {
		w.Header().Set("Request-Id", requestID)
	} else {
		requestID = ""
	}
	stream := messageStream{id: "msg_quotadeck", model: req.Model}
	if req.Stream {
		stream.emit = func(event string, data object) error {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("X-Accel-Buffering", "no")
			return writeEvent(w, event, data)
		}
	}
	if err := translateStream(response.Body, &stream); err != nil {
		var failure *inferenceError
		if !errors.As(err, &failure) {
			failure = &inferenceError{502, "api_error", "QuotaDeck could not read the OpenAI response stream"}
		}
		if req.Stream && stream.started {
			_ = writeEvent(w, "error", errorBody(failure))
		} else {
			writeGatewayError(w, failure)
		}
		return
	}
	m.mu.Lock()
	m.completed++
	m.lastRequest = requestID
	m.mu.Unlock()
	if !req.Stream {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stream.message())
	}
}

func (m *Manager) gatewayAuthorized(r *http.Request) bool {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(peer).IsLoopback() || r.Header.Get("Origin") != "" {
		return false
	}
	host := r.Host
	if name, _, err := net.SplitHostPort(host); err == nil {
		host = name
	}
	if host != "localhost" && !net.ParseIP(strings.Trim(host, "[]")).IsLoopback() {
		return false
	}
	key, err := gatewayKey(m.dir)
	if err != nil {
		return false
	}
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return subtle.ConstantTimeCompare([]byte(key), []byte(r.Header.Get("X-API-Key"))) == 1 ||
		subtle.ConstantTimeCompare([]byte(key), []byte(bearer)) == 1
}

func (m *Manager) request(ctx context.Context, method, path, token string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, m.apiURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "QuotaDeck")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
	}
	return m.apiHTTP.Do(req)
}

func readFailure(response *http.Response) *inferenceError {
	var body struct {
		Error upstreamError `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body)
	return body.Error.failure(response.StatusCode)
}

func errorBody(failure *inferenceError) object {
	return object{"type": "error", "error": object{"type": failure.kind, "message": failure.message}}
}

func writeGatewayError(w http.ResponseWriter, failure *inferenceError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(failure.status)
	_ = json.NewEncoder(w).Encode(errorBody(failure))
}
