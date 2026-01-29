package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"ok-gobot/internal/logger"
	"strings"
)

const anthropicDefaultMaxTokens = 4096
const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

// AnthropicClient implements Client for the native Anthropic Messages API.
type AnthropicClient struct {
	config     ProviderConfig
	httpClient *http.Client
}

// NewAnthropicClient creates a new Anthropic API client.
func NewAnthropicClient(config ProviderConfig) *AnthropicClient {
	if config.BaseURL == "" {
		config.BaseURL = "https://api.anthropic.com"
	}
	return &AnthropicClient{
		config: config,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Complete sends messages and returns the text response.
func (c *AnthropicClient) Complete(ctx context.Context, messages []Message) (string, error) {
	logger.Debugf("Anthropic Complete: model=%s messages=%d", c.config.Model, len(messages))

	chatMessages := ConvertLegacyMessages(messages)
	system, anthropicMsgs := translateMessages(chatMessages)

	reqBody := AnthropicRequest{
		Model:     c.config.Model,
		System:    c.buildSystem(system),
		Messages:  anthropicMsgs,
		MaxTokens: anthropicDefaultMaxTokens,
	}

	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		return "", err
	}

	text := extractText(resp)
	logger.Debugf("Anthropic Complete response: len=%d", len(text))
	return text, nil
}

// CompleteWithTools sends messages with tool definitions and returns the full response.
func (c *AnthropicClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	logger.Debugf("Anthropic CompleteWithTools: model=%s messages=%d tools=%d", c.config.Model, len(messages), len(tools))

	system, anthropicMsgs := translateMessages(messages)
	anthropicTools := translateTools(tools)

	reqBody := AnthropicRequest{
		Model:     c.config.Model,
		System:    c.buildSystem(system),
		Messages:  anthropicMsgs,
		Tools:     anthropicTools,
		MaxTokens: anthropicDefaultMaxTokens,
	}

	resp, err := c.doRequest(ctx, reqBody)
	if err != nil {
		return nil, err
	}

	result := translateResponse(resp)
	logger.Debugf("Anthropic CompleteWithTools response: tool_calls=%d", len(result.Choices[0].Message.ToolCalls))
	return result, nil
}

func (c *AnthropicClient) doRequest(ctx context.Context, reqBody AnthropicRequest) (*AnthropicResponse, error) {
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	logger.Tracef("Anthropic request body (%d bytes): %.3000s", len(jsonData), string(jsonData))

	req, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/v1/messages", bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	if isOAuthToken(c.config.APIKey) {
		// OAuth/setup-token: use Bearer auth + Claude Code headers
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
		req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14")
		req.Header.Set("user-agent", "claude-cli/2.1.2 (external, cli)")
		req.Header.Set("x-app", "cli")
	} else {
		req.Header.Set("x-api-key", c.config.APIKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	logger.Tracef("Anthropic response body (%d bytes): %.3000s", len(body), string(body))

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result AnthropicResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("API error: %s", result.Error.Message)
	}

	return &result, nil
}

// translateMessages converts ChatMessage slice to Anthropic format,
// extracting the system message to a separate string.
func translateMessages(messages []ChatMessage) (string, []AnthropicMessage) {
	var system string
	var result []AnthropicMessage

	for _, msg := range messages {
		switch msg.Role {
		case RoleSystem:
			system = msg.Content

		case RoleTool:
			// Tool results become user messages with tool_result content blocks
			result = append(result, AnthropicMessage{
				Role: RoleUser,
				Content: []ContentBlock{{
					Type:      "tool_result",
					ToolUseID: msg.ToolCallID,
					Content:   msg.Content,
				}},
			})

		case RoleAssistant:
			if len(msg.ToolCalls) > 0 {
				// Assistant with tool calls → content blocks
				var blocks []ContentBlock
				if msg.Content != "" {
					blocks = append(blocks, ContentBlock{Type: "text", Text: msg.Content})
				}
				for _, tc := range msg.ToolCalls {
					blocks = append(blocks, ContentBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Function.Name,
						Input: json.RawMessage(tc.Function.Arguments),
					})
				}
				result = append(result, AnthropicMessage{Role: RoleAssistant, Content: blocks})
			} else {
				result = append(result, AnthropicMessage{Role: RoleAssistant, Content: msg.Content})
			}

		case RoleUser:
			result = append(result, AnthropicMessage{Role: RoleUser, Content: msg.Content})
		}
	}

	return system, result
}

// translateTools converts OpenAI-format tool definitions to Anthropic format.
func translateTools(tools []ToolDefinition) []AnthropicTool {
	result := make([]AnthropicTool, len(tools))
	for i, t := range tools {
		result[i] = AnthropicTool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			InputSchema: t.Function.Parameters,
		}
	}
	return result
}

// translateResponse converts an AnthropicResponse to ChatCompletionResponse.
func translateResponse(resp *AnthropicResponse) *ChatCompletionResponse {
	var content string
	var toolCalls []ToolCall

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if content != "" {
				content += "\n"
			}
			content += block.Text
		case "tool_use":
			args, _ := json.Marshal(block.Input)
			toolCalls = append(toolCalls, ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      block.Name,
					Arguments: string(args),
				},
			})
		}
	}

	// Map stop_reason
	finishReason := "stop"
	switch resp.StopReason {
	case "end_turn":
		finishReason = "stop"
	case "tool_use":
		finishReason = "tool_calls"
	case "max_tokens":
		finishReason = "length"
	}

	totalTokens := resp.Usage.InputTokens + resp.Usage.OutputTokens

	return &ChatCompletionResponse{
		ID:    resp.ID,
		Model: resp.Model,
		Choices: []struct {
			Index        int         `json:"index"`
			Message      ChatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: ChatMessage{
					Role:      RoleAssistant,
					Content:   content,
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
		Usage: &struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      totalTokens,
		},
	}
}

// isOAuthToken checks if the key is an OAuth/setup-token (not a regular API key).
func isOAuthToken(key string) bool {
	return strings.Contains(key, "sk-ant-oat")
}

// buildSystem wraps the system prompt with Claude Code identity for OAuth tokens.
func (c *AnthropicClient) buildSystem(system string) interface{} {
	if !isOAuthToken(c.config.APIKey) {
		return system
	}
	blocks := []SystemBlock{
		{Type: "text", Text: claudeCodeIdentity, CacheControl: &CacheControl{Type: "ephemeral"}},
	}
	if system != "" {
		blocks = append(blocks, SystemBlock{Type: "text", Text: system, CacheControl: &CacheControl{Type: "ephemeral"}})
	}
	return blocks
}

// extractText returns concatenated text from content blocks.
func extractText(resp *AnthropicResponse) string {
	var text string
	for _, block := range resp.Content {
		if block.Type == "text" {
			if text != "" {
				text += "\n"
			}
			text += block.Text
		}
	}
	return text
}
