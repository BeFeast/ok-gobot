package ai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Role constants for chat messages
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ChatMessage represents a chat message with optional tool call support
type ChatMessage struct {
	Role          string         `json:"role"`
	Content       string         `json:"content,omitempty"`
	ContentBlocks []ContentBlock `json:"-"` // Internal multimodal blocks (text/image)
	ToolCalls     []ToolCall     `json:"tool_calls,omitempty"`
	ToolCallID    string         `json:"tool_call_id,omitempty"`
	Name          string         `json:"name,omitempty"` // For tool responses
	// Images are only ever received, never sent: the field exists so an
	// assistant message carrying inline images is not silently dropped.
	Images []InlineImage `json:"images,omitempty"`
}

// MarshalJSON implements custom JSON marshalling for ChatMessage.
// When ContentBlocks are present, the "content" field is serialised as an
// OpenAI-compatible multimodal array instead of a plain string.
func (m ChatMessage) MarshalJSON() ([]byte, error) {
	if len(m.ContentBlocks) == 0 {
		// Fast path: no multimodal blocks — use default struct serialisation.
		type plain ChatMessage
		return json.Marshal(plain(m))
	}

	// Build OpenAI multimodal content parts.
	parts := make([]map[string]interface{}, 0, len(m.ContentBlocks))
	for _, b := range m.ContentBlocks {
		switch b.Type {
		case "text":
			parts = append(parts, map[string]interface{}{
				"type": "text",
				"text": b.Text,
			})
		case "image":
			if b.Source != nil && b.Source.Data != "" {
				dataURL := "data:" + b.Source.MediaType + ";base64," + b.Source.Data
				parts = append(parts, map[string]interface{}{
					"type": "image_url",
					"image_url": map[string]string{
						"url": dataURL,
					},
				})
			}
		}
	}

	// Build the output map manually so "content" is the parts array.
	out := map[string]interface{}{
		"role":    m.Role,
		"content": parts,
	}
	if len(m.ToolCalls) > 0 {
		out["tool_calls"] = m.ToolCalls
	}
	if m.ToolCallID != "" {
		out["tool_call_id"] = m.ToolCallID
	}
	if m.Name != "" {
		out["name"] = m.Name
	}
	return json.Marshal(out)
}

// ToolDefinition defines a tool that the model can call
type ToolDefinition struct {
	Type     string             `json:"type"` // Always "function"
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition describes a function that can be called
type FunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ToolCall represents a tool invocation from the model
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // Always "function"
	Function FunctionCall `json:"function"`
}

// StreamToolCallDelta is one incremental tool-call fragment of a streaming
// chat completion. Index says which of several parallel tool calls the
// fragment belongs to. Without it every fragment lands on call 0 and the
// second call's arguments are appended to the first one's — the tool then
// fails with `invalid character '{' after top-level value`.
type StreamToolCallDelta struct {
	Index    *int         `json:"index,omitempty"`
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall contains the actual function call details
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ChatCompletionRequest represents the API request with tool support
type ChatCompletionRequest struct {
	Model    string           `json:"model"`
	Messages []ChatMessage    `json:"messages"`
	Tools    []ToolDefinition `json:"tools,omitempty"`
	Stream   bool             `json:"stream"`
	// ReasoningEffort carries the configured think level to reasoning-capable
	// models. Omitted when the level does not map onto the wire, which leaves
	// the provider default in place.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// InlineImage is an image the model returned inside its own message rather
// than through a tool call. Gateways fronting a natively image-capable backend
// deliver it this way: the assistant message carries no content and no tool
// calls, only an images array. Without parsing it the reply looks like empty
// model output, and the picture — already paid for and already on the wire —
// is discarded.
type InlineImage struct {
	Type     string `json:"type,omitempty"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
	Index int `json:"index,omitempty"`
}

// dataURLPattern matches the base64 data URLs used to carry inline images.
var dataURLPattern = regexp.MustCompile(`^data:(image/[a-zA-Z0-9.+-]+);base64,(.*)$`)

// Decode returns the media type and raw bytes of an inline image. Only base64
// data URLs are supported; anything else is reported rather than fetched, so a
// model can never make the bot issue an outbound request of its choosing.
func (i InlineImage) Decode() (string, []byte, error) {
	m := dataURLPattern.FindStringSubmatch(strings.TrimSpace(i.ImageURL.URL))
	if m == nil {
		return "", nil, fmt.Errorf("inline image is not a base64 data URL")
	}
	raw, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		return "", nil, fmt.Errorf("inline image base64: %w", err)
	}
	if len(raw) == 0 {
		return "", nil, fmt.Errorf("inline image is empty")
	}
	return m[1], raw, nil
}

// ChatCompletionResponse represents the API response with tool calls
type ChatCompletionResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int         `json:"index"`
		Message      ChatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage,omitempty"`
	Error *struct {
		Message string          `json:"message"`
		Type    string          `json:"type"`
		Code    json.RawMessage `json:"code,omitempty"` // Can be string or number
	} `json:"error,omitempty"`
}

// StreamChunkResponse represents a streaming response chunk with tool calls
type StreamChunkResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role      string                `json:"role,omitempty"`
			Content   string                `json:"content,omitempty"`
			Images    []InlineImage         `json:"images,omitempty"`
			ToolCalls []StreamToolCallDelta `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// StreamResult contains the complete result of a streaming request
type StreamResult struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Error        error
}
