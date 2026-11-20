package chatgpt

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayScopesAndStreaming(t *testing.T) {
	manager, key := testConnected(t)
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer authorized_test_token" {
			t.Error("gateway sent incorrect upstream endpoint or authorization")
			w.WriteHeader(400)
			return
		}
		var payload object
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil ||
			payload["store"] != false || payload["stream"] != true || payload["instructions"] != "Follow this." {
			t.Error("gateway did not build a Responses plan request")
		}
		for _, unsupported := range []string{"max_output_tokens", "metadata"} {
			if _, exists := payload[unsupported]; exists {
				t.Errorf("unexpected Responses field %s", unsupported)
			}
		}
		w.Header().Set("X-Request-Id", "req_test")
		mockResponse(w, object{"type": "response.output_item.done", "item": object{
			"type": "function_call", "name": "Edit", "call_id": "call_edit", "arguments": `{"old_string":"before","new_string":"after"}`,
		}})
	}))
	defer upstream.Close()
	manager.apiURL = upstream.URL

	body := `{"model":"gpt-test","max_tokens":500,"metadata":{"user_id":"client"},"stream":true,
		"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=foo;"},{"type":"text","text":"Follow this."}],
		"messages":[{"role":"user","content":"edit"}],"tools":[{"name":"Edit","input_schema":{"type":"object"}}]}`
	run := func(token string, origin string, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/chatgpt/v1/messages", strings.NewReader(body))
		request.RemoteAddr = "127.0.0.1:50000"
		request.Header.Set("X-API-Key", token)
		request.Header.Set("Origin", origin)
		out := httptest.NewRecorder()
		manager.Messages(out, request)
		return out
	}
	if run("wrong_key", "", body).Code != 401 || run(key, "https://example.org", body).Code != 401 || requests.Load() != 0 {
		t.Fatal("unauthorized callers reached upstream")
	}
	out := run(key, "", body)
	for _, fragment := range []string{"event: message_start", "event: content_block_start", `"id":"call_edit"`,
		`"type":"input_json_delta"`, `"stop_reason":"tool_use"`, "event: message_stop"} {
		if !strings.Contains(out.Body.String(), fragment) {
			t.Errorf("missing %q in %s", fragment, out.Body.String())
		}
	}
	out = run(key, "", strings.Replace(body, `"stream":true`, `"stream":false`, 1))
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"old_string":"before"`) {
		t.Fatalf("non-streamed response did not contain the tool input: %s", out.Body.String())
	}
	if status := manager.Status(); status.Completed != 2 || status.LastRequestID != "req_test" {
		t.Fatalf("unexpected request accounting: %+v", status)
	}

	saved, err := manager.load()
	if err != nil {
		t.Fatal(err)
	}
	saved.Scope = "openid"
	if err := writePrivate(filepath.Join(manager.dir, "credentials.json"), saved); err != nil {
		t.Fatal(err)
	}
	if run(key, "", body).Code != 401 || requests.Load() != 2 {
		t.Fatal("request without plan permission reached upstream")
	}
}

func TestClaudeCodeEditsFileThroughGateway(t *testing.T) {
	binary := os.Getenv("QUOTADECK_TEST_BINARY")
	if binary == "" {
		t.Skip("set QUOTADECK_TEST_BINARY to test a built quotadeck with an installed Claude Code")
	}
	manager, _ := testConnected(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "fixture.txt")
	const before, after = "original", "edited through QuotaDeck"
	if err := os.WriteFile(file, []byte(before+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var edits atomic.Int32
	var results atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer authorized_test_token" {
			t.Error("gateway did not use the plan OAuth token")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(object{"models": []Model{{Slug: "gpt-test", DisplayName: "Test", Visibility: "list"}}})
			return
		}
		var payload struct {
			Model string   `json:"model"`
			Input []object `json:"input"`
			Tools []object `json:"tools"`
		}
		if r.URL.Path != "/responses" || json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Model != "gpt-test" {
			t.Error("unexpected Claude Code request")
			w.WriteHeader(400)
			return
		}
		read, edited := false, false
		for _, item := range payload.Input {
			if item["type"] == "function_call_output" {
				switch item["call_id"] {
				case "call_read":
					read = strings.Contains(fmt.Sprint(item["output"]), before)
				case "call_edit":
					edited = true
				}
			}
		}
		if edited {
			results.Add(1)
			mockResponse(w, object{"type": "response.output_text.delta", "delta": "FILE_EDIT_OK"})
			return
		}
		name, id := "Read", "call_read"
		arguments := object{"file_path": file}
		if read {
			edits.Add(1)
			name, id = "Edit", "call_edit"
			arguments["old_string"], arguments["new_string"] = before, after
		}
		for _, tool := range payload.Tools {
			if tool["name"] == name {
				body, _ := json.Marshal(arguments)
				mockResponse(w, object{"type": "response.output_item.done", "item": object{
					"type": "function_call", "name": name, "call_id": id, "arguments": string(body),
				}})
				return
			}
		}
		// Claude Code sometimes sends a separate analysis or naming request with no tools.
		mockResponse(w, object{"type": "response.output_text.delta", "delta": "QuotaDeck check"})
	}))
	defer upstream.Close()
	manager.apiURL = upstream.URL
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+GatewayPath+"/v1/messages", manager.Messages)
	mux.HandleFunc("GET /api/v1/control/chatgpt/models", func(w http.ResponseWriter, r *http.Request) {
		models, err := manager.Models(r.Context())
		if err != nil {
			t.Error(err)
			w.WriteHeader(502)
			return
		}
		_ = json.NewEncoder(w).Encode(models)
	})
	daemon := httptest.NewServer(mux)
	defer daemon.Close()
	conf := filepath.Join(dir, "config.yaml")
	config := fmt.Sprintf("server:\n  port: %s\nstorage:\n  database: %s\n",
		strings.Split(daemon.URL, ":")[2], strings.TrimSuffix(manager.dir, ".chatgpt"))
	if err := os.WriteFile(conf, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	claudeConfig := filepath.Join(dir, "claude")
	if err := os.Mkdir(claudeConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	// A previously selected provider in the user's settings must not override
	// the QuotaDeck session.
	const previousProvider = `{"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:1","ANTHROPIC_API_KEY":"previous-test-key","ANTHROPIC_MODEL":"previous-model"}}`
	if err := os.WriteFile(filepath.Join(claudeConfig, "settings.json"), []byte(previousProvider), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "claude", "--config", conf, "--", "--no-session-persistence",
		"--permission-mode", "acceptEdits", "--tools", "Read,Edit", "-p", "Read and edit fixture.txt, then confirm.")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+claudeConfig, "CLAUDECODE=", "CLAUDE_CODE_ENTRYPOINT=")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Claude Code exited with %v: %s", err, output)
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != after+"\n" || edits.Load() == 0 || results.Load() == 0 ||
		!strings.Contains(string(output), "FILE_EDIT_OK") {
		t.Fatalf("edit did not round-trip: file=%q, edits=%d, results=%d, Claude Code output=%q", got, edits.Load(), results.Load(), output)
	}
	settings, err := os.ReadFile(filepath.Join(claudeConfig, "settings.json"))
	if err != nil || string(settings) != previousProvider {
		t.Fatal("the launcher changed the user's Claude Code settings")
	}
}

func testConnected(t *testing.T) (*Manager, string) {
	t.Helper()
	manager := New(filepath.Join(t.TempDir(), "state.db"))
	t.Cleanup(manager.Close)
	if err := manager.ensureGatewayKey(); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(filepath.Join(manager.dir, "credentials.json"), credential{
		tokens:   tokens{AccessToken: "authorized_test_token", Scope: planScope},
		ClientID: "oaiapp_test", Subject: "test-subject", Expires: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	key, err := gatewayKey(manager.dir)
	if err != nil {
		t.Fatal(err)
	}
	return manager, key
}

func mockResponse(w http.ResponseWriter, events ...object) {
	w.Header().Set("Content-Type", "text/event-stream")
	all := append([]object{{"type": "response.created", "response": object{"id": "resp_mock"}}}, events...)
	all = append(all, object{"type": "response.completed", "response": object{
		"id": "resp_mock", "usage": object{"input_tokens": 20, "output_tokens": 10},
	}})
	for _, item := range all {
		bytes, _ := json.Marshal(item)
		_, _ = io.WriteString(w, "data: "+string(bytes)+"\n\n")
	}
}
