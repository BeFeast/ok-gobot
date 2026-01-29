package ai

import "encoding/json"

// AnthropicRequest represents a request to the Anthropic Messages API.
type AnthropicRequest struct {
	Model     string             `json:"model"`
	System    interface{}        `json:"system,omitempty"` // string or []SystemBlock for OAuth
	Messages  []AnthropicMessage `json:"messages"`
	Tools     []AnthropicTool    `json:"tools,omitempty"`
	MaxTokens int                `json:"max_tokens"`
}

// SystemBlock represents a system prompt block with optional cache control.
type SystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// CacheControl sets caching behavior for a block.
type CacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// AnthropicMessage represents a message in the Anthropic format.
type AnthropicMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or []ContentBlock
}

// ContentBlock represents a block within Anthropic message content.
type ContentBlock struct {
	Type      string          `json:"type"`                 // "text", "tool_use", "tool_result"
	Text      string          `json:"text,omitempty"`       // for type="text"
	ID        string          `json:"id,omitempty"`         // for type="tool_use"
	Name      string          `json:"name,omitempty"`       // for type="tool_use"
	Input     json.RawMessage `json:"input,omitempty"`      // for type="tool_use"
	ToolUseID string          `json:"tool_use_id,omitempty"` // for type="tool_result"
	Content   string          `json:"content,omitempty"`    // for type="tool_result"
}

// AnthropicResponse represents the Anthropic Messages API response.
type AnthropicResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Content    []ContentBlock `json:"content"`
	Model      string         `json:"model"`
	StopReason string         `json:"stop_reason"`
	Usage      AnthropicUsage `json:"usage"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// AnthropicUsage represents token usage in the Anthropic response.
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicTool represents a tool definition in Anthropic format.
type AnthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}
