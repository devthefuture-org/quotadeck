package chatgpt

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestConvertConversationPreservesAssistantTextAndToolHistory(t *testing.T) {
	var req messagesRequest
	err := json.Unmarshal([]byte(`{
		"model": "gpt-test",
		"messages": [
			{"role": "user", "content": "Read the file."},
			{"role": "assistant", "content": [
				{"type": "text", "text": "I will read it."},
				{"type": "tool_use", "id": "toolu_demo", "name": "Read", "input": {"file_path": "fixture.txt"}}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_demo", "content": "demo result"}
			]},
			{"role": "assistant", "content": "I read it."},
			{"role": "system", "content": "Use local file permissions."},
			{"role": "user", "content": "What did it contain?"}
		]
	}`), &req)
	if err != nil {
		t.Fatal(err)
	}
	got, err := req.convert()
	if err != nil {
		t.Fatal(err)
	}
	message := func(role, kind, text string) object {
		return object{"type": "message", "role": role, "content": []object{{"type": kind, "text": text}}}
	}
	want := []object{
		message("user", "input_text", "Read the file."),
		message("assistant", "output_text", "I will read it."),
		{"type": "function_call", "call_id": "toolu_demo", "name": "Read", "arguments": `{"file_path": "fixture.txt"}`},
		{"type": "function_call_output", "call_id": "toolu_demo", "output": "demo result"},
		message("assistant", "output_text", "I read it."),
		message("developer", "input_text", "Use local file permissions."),
		message("user", "input_text", "What did it contain?"),
	}
	if !reflect.DeepEqual(got["input"], want) {
		t.Fatalf("unexpected Responses history: %#v", got["input"])
	}
}

func TestGatewayErrorsKeepStructuredDetails(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body: io.NopCloser(strings.NewReader(`{"error":{
			"message":"private request content", "code":"invalid_value", "param":"input[1].content[0]"
		}}`)),
	}
	failure := readFailure(response)
	if failure.status != 400 || !strings.Contains(failure.Error(), "invalid_value") ||
		!strings.Contains(failure.Error(), "input[1].content[0]") || strings.Contains(failure.Error(), "private") {
		t.Fatalf("unexpected error returned to Claude Code: %v", failure)
	}

	stream := strings.NewReader("data: {\"type\":\"error\",\"code\":\"invalid_value\",\"param\":\"tools[0].name\"}\n\n")
	err := translateStream(stream, &messageStream{})
	if err == nil || !strings.Contains(err.Error(), "tools[0].name") {
		t.Fatalf("streamed rejection lost its field path: %v", err)
	}
	response = &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"private request content","code":"server_error"}}`)),
	}
	failure = readFailure(response)
	if failure.status != 502 || !strings.Contains(failure.Error(), "HTTP status (500)") ||
		!strings.Contains(failure.Error(), "server_error") || strings.Contains(failure.Error(), "private") {
		t.Fatalf("upstream server failure lost its status or exposed request content: %v", failure)
	}
}

func TestGatewayDistinguishesUnfinishedResponses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		event  string
		status int
		want   string
	}{
		{"disconnect", "", 502, "closed the response stream"},
		{"upstream server failure", `{"type":"response.failed","response":{"error":{"code":"server_error","message":"private request content"}}}`, 502, "server_error"},
		{"incomplete response", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"content_filter"}}}`, 502, "incomplete response · content_filter"},
		{"context limit", `{"type":"response.failed","response":{"error":{"code":"context_length_exceeded"}}}`, 400, "context_length_exceeded"},
		{"rate limit", `{"type":"error","code":"rate_limit_exceeded"}`, 429, "rate_limit_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\"}}\n\n"
			if tc.event != "" {
				body += "data: " + tc.event + "\n\n"
			}
			var failure *inferenceError
			err := translateStream(strings.NewReader(body), &messageStream{})
			if !errors.As(err, &failure) || failure.status != tc.status ||
				!strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private request content") {
				t.Fatalf("unexpected error returned to Claude Code: %v", err)
			}
		})
	}
}
