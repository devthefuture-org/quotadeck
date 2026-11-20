package chatgpt

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

type responseUsage struct {
	Input  int `json:"input_tokens"`
	Output int `json:"output_tokens"`
}

type upstreamError struct {
	Code  string `json:"code"`
	Param string `json:"param"`
}

type outputItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responseEvent struct {
	Type         string     `json:"type"`
	Delta        string     `json:"delta"`
	OutputIndex  int        `json:"output_index"`
	ContentIndex int        `json:"content_index"`
	Item         outputItem `json:"item"`
	Code         string     `json:"code"`
	Param        string     `json:"param"`
	Response     struct {
		ID                string        `json:"id"`
		Usage             responseUsage `json:"usage"`
		Error             upstreamError `json:"error"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

type blockPosition struct {
	OutputIndex  int
	ContentIndex int
	Tool         bool
}

type messageBlock struct {
	position blockPosition
	data     object
	parts    strings.Builder
	closed   bool
}

type messageStream struct {
	id      string
	model   string
	blocks  []*messageBlock
	hasTool bool
	usage   responseUsage
	emit    func(string, object) error
	started bool
}

func (s *messageStream) initialMessage() object {
	return object{
		"id": s.id, "type": "message", "role": "assistant", "model": s.model,
		"content": []object{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": responseUsage{},
	}
}

func (s *messageStream) send(kind string, data object) error {
	if s.emit == nil {
		return nil
	}
	data["type"] = kind
	return s.emit(kind, data)
}

func (s *messageStream) start(id string) error {
	if s.started {
		return nil
	}
	if id != "" {
		s.id = id
	}
	s.started = true
	return s.send("message_start", object{"message": s.initialMessage()})
}

func (s *messageStream) find(position blockPosition) (int, *messageBlock) {
	for i, block := range s.blocks {
		if block.position == position {
			return i, block
		}
	}
	return -1, nil
}

func (s *messageStream) begin(position blockPosition, data object) (int, *messageBlock, error) {
	if err := s.start(""); err != nil {
		return 0, nil, err
	}
	index := len(s.blocks)
	block := &messageBlock{position: position, data: data}
	s.blocks = append(s.blocks, block)
	return index, block, s.send("content_block_start", object{"index": index, "content_block": data})
}

func (s *messageStream) text(position blockPosition, text string) error {
	index, block := s.find(position)
	if block == nil {
		var err error
		index, block, err = s.begin(position, object{"type": "text", "text": ""})
		if err != nil {
			return err
		}
	}
	if block.closed {
		return errors.New("OpenAI sent text after a content block ended")
	}
	block.parts.WriteString(text)
	return s.send("content_block_delta", object{"index": index, "delta": object{"type": "text_delta", "text": text}})
}

func (s *messageStream) tool(position blockPosition, item outputItem) error {
	var args object
	if item.CallID == "" || item.Name == "" || json.Unmarshal([]byte(item.Arguments), &args) != nil || args == nil {
		return errors.New("OpenAI returned an invalid function call")
	}
	index, block, err := s.begin(position, object{"type": "tool_use", "id": item.CallID, "name": item.Name, "input": object{}})
	if err != nil {
		return err
	}
	block.data["input"] = args
	s.hasTool = true
	if err := s.send("content_block_delta", object{"index": index, "delta": object{"type": "input_json_delta", "partial_json": item.Arguments}}); err != nil {
		return err
	}
	return s.endBlock(index, block)
}

func (s *messageStream) endBlock(index int, block *messageBlock) error {
	if block.closed {
		return nil
	}
	block.closed = true
	if !block.position.Tool {
		block.data["text"] = block.parts.String()
	}
	return s.send("content_block_stop", object{"index": index})
}

func (s *messageStream) stopReason() string {
	if s.hasTool {
		return "tool_use"
	}
	return "end_turn"
}

func (s *messageStream) complete(event responseEvent) error {
	if err := s.start(event.Response.ID); err != nil {
		return err
	}
	for index, block := range s.blocks {
		if err := s.endBlock(index, block); err != nil {
			return err
		}
	}
	s.usage = event.Response.Usage
	if err := s.send("message_delta", object{
		"delta": object{"stop_reason": s.stopReason(), "stop_sequence": nil}, "usage": s.usage,
	}); err != nil {
		return err
	}
	return s.send("message_stop", object{})
}

func (s *messageStream) message() object {
	result := s.initialMessage()
	content := make([]object, 0, len(s.blocks))
	for _, block := range s.blocks {
		content = append(content, block.data)
	}
	result["content"] = content
	result["usage"] = s.usage
	result["stop_reason"] = s.stopReason()
	return result
}

type inferenceError struct {
	status  int
	kind    string
	message string
}

func (e *inferenceError) Error() string { return e.message }

var (
	upstreamCodePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,79}$`)
	upstreamParamPattern = regexp.MustCompile(`^(input|instructions|model|tools|tool_choice|parallel_tool_calls|stream|store)([.\[][a-zA-Z0-9_.\[\]-]{1,95})?$`)
)

func (e upstreamError) failure(status int) *inferenceError {
	if status == 0 {
		switch e.Code {
		case "invalid_request_error", "invalid_value", "missing_required_parameter", "context_length_exceeded", "invalid_prompt":
			status = http.StatusBadRequest
		case "rate_limit_exceeded":
			status = http.StatusTooManyRequests
		}
	}
	failure := gatewayFailure(e.Code, status)
	// Upstream free-text errors can quote conversation content.
	if upstreamCodePattern.MatchString(e.Code) && !strings.HasPrefix(failure.message, e.Code) {
		failure.message += " · " + e.Code
	}
	if failure.status == http.StatusBadRequest || failure.status == http.StatusNotFound {
		if upstreamParamPattern.MatchString(e.Param) {
			failure.message += " · field: " + e.Param
		}
	}
	return failure
}

func gatewayFailure(code string, status int) *inferenceError {
	switch code {
	case "subscription_sharing_usage_limit_exceeded":
		return &inferenceError{429, "rate_limit_error", code + ": check ChatGPT Settings → Usage"}
	case "subscription_sharing_user_not_eligible":
		return &inferenceError{403, "permission_error", code + ": this account or workspace cannot use the plan"}
	case "subscription_sharing_unsupported_capability":
		return &inferenceError{400, "invalid_request_error", code + ": this request uses an unsupported capability"}
	case "subscription_sharing_usage_unavailable":
		return &inferenceError{503, "overloaded_error", code + ": ChatGPT usage could not be checked"}
	}
	switch status {
	case 400, 404:
		return &inferenceError{status, "invalid_request_error", fmt.Sprintf("OpenAI rejected this request (HTTP %d)", status)}
	case 401, 403:
		return &inferenceError{status, "authentication_error", "OpenAI rejected the ChatGPT connection. Sign in again in QuotaDeck."}
	case 429:
		return &inferenceError{status, "rate_limit_error", "OpenAI rate limit reached. Check ChatGPT Settings → Usage."}
	case 503:
		return &inferenceError{status, "overloaded_error", "OpenAI is temporarily unavailable"}
	case 0:
		return &inferenceError{502, "api_error", "OpenAI reported a failure in the response stream"}
	default:
		return &inferenceError{502, "api_error", fmt.Sprintf("OpenAI returned an unexpected HTTP status (%d)", status)}
	}
}

func translateStream(body io.Reader, s *messageStream) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 16<<10), 32<<20)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
			continue
		}
		if line != "" || len(data) == 0 {
			continue
		}
		raw := strings.Join(data, "\n")
		data = data[:0]
		if raw == "[DONE]" {
			break
		}
		var event responseEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return errors.New("OpenAI sent an invalid streamed event")
		}
		position := blockPosition{OutputIndex: event.OutputIndex, ContentIndex: event.ContentIndex}
		var err error
		switch event.Type {
		case "response.created":
			err = s.start(event.Response.ID)
		case "response.output_text.delta", "response.refusal.delta":
			err = s.text(position, event.Delta)
		case "response.output_text.done", "response.refusal.done":
			if index, block := s.find(position); block != nil {
				err = s.endBlock(index, block)
			}
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				err = s.tool(blockPosition{OutputIndex: event.OutputIndex, Tool: true}, event.Item)
			}
		case "response.completed":
			return s.complete(event)
		case "response.incomplete":
			if event.Response.Error.Code != "" {
				return event.Response.Error.failure(0)
			}
			message := "OpenAI returned an incomplete response"
			switch reason := event.Response.IncompleteDetails.Reason; reason {
			case "content_filter", "max_output_tokens", "max_messages", "steered":
				message += " · " + reason
			}
			return &inferenceError{502, "api_error", message}
		case "response.failed", "error":
			failure := event.Response.Error
			if failure.Code == "" {
				failure.Code = event.Code
			}
			if failure.Param == "" {
				failure.Param = event.Param
			}
			return failure.failure(0)
		case "response.reasoning_summary_text.delta", "response.in_progress":
			if s.started {
				err = s.send("ping", object{})
			}
		}
		if err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return &inferenceError{502, "api_error", "OpenAI closed the response stream before sending a final result"}
}

func writeEvent(w http.ResponseWriter, event string, data object) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	return http.NewResponseController(w).Flush()
}
