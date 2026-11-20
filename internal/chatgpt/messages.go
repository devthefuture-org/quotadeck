package chatgpt

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type object = map[string]any

type messagesRequest struct {
	Model    string          `json:"model"`
	System   json.RawMessage `json:"system"`
	Stream   bool            `json:"stream"`
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Tools []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Schema      json.RawMessage `json:"input_schema"`
	} `json:"tools"`
	ToolChoice struct {
		Type            string `json:"type"`
		Name            string `json:"name"`
		DisableParallel bool   `json:"disable_parallel_tool_use"`
	} `json:"tool_choice"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

func decodeContent(data json.RawMessage) ([]contentBlock, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		return []contentBlock{{Type: "text", Text: text}}, nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(data, &blocks); err != nil {
		return nil, errors.New("message content must be text or content blocks")
	}
	return blocks, nil
}

func (r messagesRequest) convert() (object, error) {
	if strings.TrimSpace(r.Model) == "" || len(r.Messages) == 0 {
		return nil, errors.New("a model and messages are required")
	}
	system, err := decodeContent(r.System)
	if err != nil {
		return nil, err
	}
	var instructions []string
	for _, block := range system {
		if block.Type != "text" {
			return nil, errors.New("only text system instructions are supported")
		}
		if !strings.HasPrefix(block.Text, "x-anthropic-billing-header:") {
			instructions = append(instructions, block.Text)
		}
	}
	input := make([]object, 0, len(r.Messages))
	for _, message := range r.Messages {
		items, err := convertMessage(message.Role, message.Content)
		if err != nil {
			return nil, err
		}
		input = append(input, items...)
	}
	if len(input) == 0 {
		return nil, errors.New("at least one supported message is required")
	}
	payload := object{"model": r.Model, "input": input, "stream": true, "store": false}
	if len(instructions) > 0 {
		payload["instructions"] = strings.Join(instructions, "\n\n")
	}
	tools := make([]object, 0, len(r.Tools))
	for _, tool := range r.Tools {
		// A hosted Anthropic tool has a type but no client-side input schema.
		if tool.Name == "" || len(tool.Schema) == 0 {
			continue
		}
		tools = append(tools, object{
			"type": "function", "name": tool.Name, "description": tool.Description,
			"parameters": tool.Schema, "strict": false,
		})
	}
	if len(tools) > 0 {
		payload["tools"] = tools
		payload["parallel_tool_calls"] = !r.ToolChoice.DisableParallel
		switch r.ToolChoice.Type {
		case "", "auto":
		case "none":
			payload["tool_choice"] = "none"
		case "any":
			payload["tool_choice"] = "required"
		case "tool":
			payload["tool_choice"] = object{"type": "function", "name": r.ToolChoice.Name}
		default:
			return nil, errors.New("unsupported tool choice")
		}
	}
	return payload, nil
}

func convertMessage(role string, raw json.RawMessage) ([]object, error) {
	blocks, err := decodeContent(raw)
	if err != nil {
		return nil, err
	}
	if role == "system" {
		role = "developer"
	}
	if role != "user" && role != "assistant" && role != "developer" {
		return nil, fmt.Errorf("unsupported message role %q", role)
	}
	var items, contents []object
	textType := "input_text"
	if role == "assistant" {
		textType = "output_text"
	}
	flush := func() {
		if len(contents) != 0 {
			items = append(items, object{"type": "message", "role": role, "content": contents})
			contents = nil
		}
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if role != "developer" || !strings.HasPrefix(block.Text, "x-anthropic-billing-header:") {
				contents = append(contents, object{"type": textType, "text": block.Text})
			}
		case "image":
			image, err := convertImage(block)
			if err != nil || role != "user" {
				return nil, errors.New("only base64 user images are supported")
			}
			contents = append(contents, image)
		case "tool_use":
			var args map[string]any
			if role != "assistant" || block.ID == "" || block.Name == "" ||
				json.Unmarshal(block.Input, &args) != nil || args == nil {
				return nil, errors.New("invalid tool use")
			}
			flush()
			items = append(items, object{
				"type": "function_call", "call_id": block.ID, "name": block.Name, "arguments": string(block.Input),
			})
		case "tool_result":
			if role != "user" || block.ToolUseID == "" {
				return nil, errors.New("invalid tool result")
			}
			result, err := convertToolResult(block)
			if err != nil {
				return nil, err
			}
			flush()
			items = append(items, object{"type": "function_call_output", "call_id": block.ToolUseID, "output": result})
		case "thinking", "redacted_thinking":
			// Anthropic reasoning signatures have no corresponding OpenAI item.
		default:
			return nil, fmt.Errorf("unsupported message content type %q", block.Type)
		}
	}
	flush()
	return items, nil
}

func convertImage(block contentBlock) (object, error) {
	if block.Source.Type != "base64" || !strings.HasPrefix(block.Source.MediaType, "image/") || block.Source.Data == "" {
		return nil, errors.New("invalid inline image")
	}
	return object{"type": "input_image", "image_url": "data:" + block.Source.MediaType + ";base64," + block.Source.Data, "detail": "auto"}, nil
}

func convertToolResult(block contentBlock) (any, error) {
	blocks, err := decodeContent(block.Content)
	if err != nil {
		return nil, err
	}
	contents := make([]object, 0, len(blocks)+1)
	if block.IsError {
		contents = append(contents, object{"type": "input_text", "text": "Tool execution failed."})
	}
	for _, part := range blocks {
		switch part.Type {
		case "text":
			contents = append(contents, object{"type": "input_text", "text": part.Text})
		case "image":
			image, err := convertImage(part)
			if err != nil {
				return nil, err
			}
			contents = append(contents, image)
		default:
			return nil, fmt.Errorf("unsupported tool result content type %q", part.Type)
		}
	}
	if len(contents) == 0 {
		return "", nil
	}
	if len(contents) == 1 && contents[0]["type"] == "input_text" {
		return contents[0]["text"], nil
	}
	return contents, nil
}
