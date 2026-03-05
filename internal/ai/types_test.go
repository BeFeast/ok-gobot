package ai

import (
	"encoding/json"
	"testing"
)

func TestChatMessageMarshaling(t *testing.T) {
	tests := []struct {
		name    string
		message ChatMessage
		wantErr bool
	}{
		{
			name: "simple user message",
			message: ChatMessage{
				Role:    RoleUser,
				Content: "Hello, world!",
			},
			wantErr: false,
		},
		{
			name: "assistant message with tool calls",
			message: ChatMessage{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{
					{
						ID:   "call_abc123",
						Type: "function",
						Function: FunctionCall{
							Name:      "get_weather",
							Arguments: `{"location":"San Francisco"}`,
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "tool response message",
			message: ChatMessage{
				Role:       RoleTool,
				Content:    `{"temperature":72,"condition":"sunny"}`,
				ToolCallID: "call_abc123",
				Name:       "get_weather",
			},
			wantErr: false,
		},
		{
			name: "multiple tool calls",
			message: ChatMessage{
				Role: RoleAssistant,
				ToolCalls: []ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: FunctionCall{
							Name:      "search",
							Arguments: `{"query":"weather"}`,
						},
					},
					{
						ID:   "call_2",
						Type: "function",
						Function: FunctionCall{
							Name:      "get_time",
							Arguments: `{}`,
						},
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal to JSON
			data, err := json.Marshal(tt.message)
			if (err != nil) != tt.wantErr {
				t.Errorf("Marshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			// Unmarshal back
			var got ChatMessage
			if err := json.Unmarshal(data, &got); err != nil {
				t.Errorf("Unmarshal() error = %v", err)
				return
			}

			// Compare
			if got.Role != tt.message.Role {
				t.Errorf("Role = %v, want %v", got.Role, tt.message.Role)
			}
			if got.Content != tt.message.Content {
				t.Errorf("Content = %v, want %v", got.Content, tt.message.Content)
			}
			if len(got.ToolCalls) != len(tt.message.ToolCalls) {
				t.Errorf("ToolCalls length = %v, want %v", len(got.ToolCalls), len(tt.message.ToolCalls))
			}
			if got.ToolCallID != tt.message.ToolCallID {
				t.Errorf("ToolCallID = %v, want %v", got.ToolCallID, tt.message.ToolCallID)
			}
		})
	}
}

func TestChatMessageMarshalJSON_WithContentBlocks(t *testing.T) {
	msg := ChatMessage{
		Role:    RoleUser,
		Content: "describe this image",
		ContentBlocks: []ContentBlock{
			{Type: "text", Text: "describe this image"},
			{
				Type: "image",
				Source: &ContentSource{
					Type:      "base64",
					MediaType: "image/png",
					Data:      "iVBORw0KGgo=",
				},
			},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	// Parse back as generic map to inspect structure.
	var got map[string]interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got["role"] != "user" {
		t.Errorf("role = %v, want user", got["role"])
	}

	content, ok := got["content"].([]interface{})
	if !ok {
		t.Fatalf("content should be an array, got %T", got["content"])
	}
	if len(content) != 2 {
		t.Fatalf("content length = %d, want 2", len(content))
	}

	// First part: text
	textPart := content[0].(map[string]interface{})
	if textPart["type"] != "text" {
		t.Errorf("first part type = %v, want text", textPart["type"])
	}
	if textPart["text"] != "describe this image" {
		t.Errorf("first part text = %v", textPart["text"])
	}

	// Second part: image_url
	imgPart := content[1].(map[string]interface{})
	if imgPart["type"] != "image_url" {
		t.Errorf("second part type = %v, want image_url", imgPart["type"])
	}
	imgURL := imgPart["image_url"].(map[string]interface{})
	wantURL := "data:image/png;base64,iVBORw0KGgo="
	if imgURL["url"] != wantURL {
		t.Errorf("image url = %v, want %v", imgURL["url"], wantURL)
	}
}

func TestChatMessageMarshalJSON_WithoutContentBlocks(t *testing.T) {
	msg := ChatMessage{
		Role:    RoleUser,
		Content: "hello",
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got map[string]interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	// Content should be a plain string, not an array.
	if _, ok := got["content"].(string); !ok {
		t.Fatalf("content should be a string when no ContentBlocks, got %T", got["content"])
	}
}

func TestToolDefinitionMarshaling(t *testing.T) {
	tests := []struct {
		name       string
		definition ToolDefinition
		wantJSON   string
	}{
		{
			name: "simple tool definition",
			definition: ToolDefinition{
				Type: "function",
				Function: FunctionDefinition{
					Name:        "get_weather",
					Description: "Get the current weather for a location",
					Parameters:  json.RawMessage(`{"type":"object","properties":{"location":{"type":"string","description":"City name"}},"required":["location"]}`),
				},
			},
			wantJSON: `{"type":"function","function":{"name":"get_weather","description":"Get the current weather for a location","parameters":{"type":"object","properties":{"location":{"type":"string","description":"City name"}},"required":["location"]}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal to JSON
			data, err := json.Marshal(tt.definition)
			if err != nil {
				t.Errorf("Marshal() error = %v", err)
				return
			}

			// Compare JSON (normalize for comparison)
			var want, got interface{}
			if err := json.Unmarshal([]byte(tt.wantJSON), &want); err != nil {
				t.Errorf("Unmarshal wantJSON error = %v", err)
				return
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Errorf("Unmarshal got error = %v", err)
				return
			}

			wantBytes, _ := json.Marshal(want)
			gotBytes, _ := json.Marshal(got)

			if string(wantBytes) != string(gotBytes) {
				t.Errorf("JSON mismatch:\nwant: %s\ngot:  %s", wantBytes, gotBytes)
			}
		})
	}
}

func TestStreamChunkResponseUnmarshaling(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
	}{
		{
			name: "content chunk",
			json: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		},
		{
			name: "tool call chunk with function name",
			json: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}`,
		},
		{
			name: "tool call chunk with arguments",
			json: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"location\""}}]},"finish_reason":null}]}`,
		},
		{
			name: "finish chunk",
			json: `{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var chunk StreamChunkResponse
			err := json.Unmarshal([]byte(tt.json), &chunk)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && len(chunk.Choices) == 0 {
				t.Error("Expected at least one choice in response")
			}
		})
	}
}

func TestChatCompletionRequestMarshaling(t *testing.T) {
	req := ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []ChatMessage{
			{
				Role:    RoleUser,
				Content: "What's the weather?",
			},
		},
		Tools: []ToolDefinition{
			{
				Type: "function",
				Function: FunctionDefinition{
					Name:        "get_weather",
					Description: "Get weather",
					Parameters:  json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`),
				},
			},
		},
		Stream: false,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got ChatCompletionRequest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got.Model != req.Model {
		t.Errorf("Model = %v, want %v", got.Model, req.Model)
	}
	if len(got.Messages) != len(req.Messages) {
		t.Errorf("Messages length = %v, want %v", len(got.Messages), len(req.Messages))
	}
	if len(got.Tools) != len(req.Tools) {
		t.Errorf("Tools length = %v, want %v", len(got.Tools), len(req.Tools))
	}
}
