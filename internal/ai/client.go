package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ok-gobot/internal/logger"
)

// Message represents a chat message (legacy, kept for backward compatibility)
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// StreamChunk represents a piece of streamed response
type StreamChunk struct {
	Content      string
	Done         bool
	FinishReason string
	Error        error
}

// StreamingClient extends Client with streaming support
type StreamingClient interface {
	Client
	CompleteStream(ctx context.Context, messages []Message) <-chan StreamChunk
	CompleteStreamWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) <-chan StreamChunk
}

// Client defines the interface for AI providers
type Client interface {
	Complete(ctx context.Context, messages []Message) (string, error)
	CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error)
}

// ProviderConfig holds configuration for an AI provider
type ProviderConfig struct {
	Name    string
	APIKey  string
	BaseURL string
	Model   string
}

// OpenAICompatibleClient implements Client for OpenAI-compatible APIs
// Works with: OpenAI, OpenRouter, Anyscale, Together, etc.
type OpenAICompatibleClient struct {
	config     ProviderConfig
	httpClient *http.Client
}

// NewClient creates a new AI client from provider configuration
func NewClient(config ProviderConfig) (*OpenAICompatibleClient, error) {
	// Set defaults
	if config.BaseURL == "" {
		switch config.Name {
		case "openai":
			config.BaseURL = "https://api.openai.com/v1"
		case "openrouter":
			config.BaseURL = "https://openrouter.ai/api/v1"
		default:
			return nil, fmt.Errorf("unknown provider: %s (specify BaseURL)", config.Name)
		}
	}

	if config.Model == "" {
		config.Model = "gpt-4o"
	}

	return &OpenAICompatibleClient{
		config: config,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}, nil
}

// chatCompletionRequest represents the API request body (legacy)
type chatCompletionRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Stream   bool      `json:"stream"`
}

// chatCompletionResponse represents the API response (legacy)
type chatCompletionResponse struct {
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Complete sends messages and returns the response
func (c *OpenAICompatibleClient) Complete(ctx context.Context, messages []Message) (string, error) {
	logger.Debugf("AI Complete: model=%s messages=%d", c.config.Model, len(messages))
	reqBody := chatCompletionRequest{
		Model:    c.config.Model,
		Messages: messages,
		Stream:   false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		c.config.BaseURL+"/chat/completions",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	// OpenRouter-specific headers
	if c.config.Name == "openrouter" {
		req.Header.Set("HTTP-Referer", "https://github.com/BeFeast/ok-gobot")
		req.Header.Set("X-Title", "ok-gobot")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result chatCompletionResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if result.Error != nil {
		return "", fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no response from model")
	}

	content := result.Choices[0].Message.Content
	logger.Debugf("AI Complete response: len=%d", len(content))
	return content, nil
}

// CompleteWithTools sends messages with tool definitions and returns the full response
func (c *OpenAICompatibleClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	logger.Debugf("AI CompleteWithTools: model=%s messages=%d tools=%d", c.config.Model, len(messages), len(tools))
	reqBody := ChatCompletionRequest{
		Model:    c.config.Model,
		Messages: messages,
		Tools:    tools,
		Stream:   false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	logger.Tracef("AI CompleteWithTools request body (%d bytes): %.3000s", len(jsonData), string(jsonData))

	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		c.config.BaseURL+"/chat/completions",
		bytes.NewBuffer(jsonData),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)

	// OpenRouter-specific headers
	if c.config.Name == "openrouter" {
		req.Header.Set("HTTP-Referer", "https://github.com/BeFeast/ok-gobot")
		req.Header.Set("X-Title", "ok-gobot")
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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result ChatCompletionResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if result.Error != nil {
		return nil, fmt.Errorf("API error: %s", result.Error.Message)
	}

	if len(result.Choices) == 0 {
		return nil, fmt.Errorf("no response from model")
	}

	logger.Tracef("AI CompleteWithTools response body (%d bytes): %.3000s", len(body), string(body))
	logger.Debugf("AI CompleteWithTools response: choices=%d tool_calls=%d", len(result.Choices), func() int {
		if len(result.Choices) > 0 {
			return len(result.Choices[0].Message.ToolCalls)
		}
		return 0
	}())
	return &result, nil
}

// streamChunkResponse represents a single SSE chunk from the streaming API (legacy)
type streamChunkResponse struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// CompleteStream sends messages and returns a channel of streamed chunks
func (c *OpenAICompatibleClient) CompleteStream(ctx context.Context, messages []Message) <-chan StreamChunk {
	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)

		reqBody := chatCompletionRequest{
			Model:    c.config.Model,
			Messages: messages,
			Stream:   true,
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		req, err := http.NewRequestWithContext(
			ctx,
			"POST",
			c.config.BaseURL+"/chat/completions",
			bytes.NewBuffer(jsonData),
		)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to create request: %w", err)}
			return
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
		req.Header.Set("Accept", "text/event-stream")

		if c.config.Name == "openrouter" {
			req.Header.Set("HTTP-Referer", "https://github.com/BeFeast/ok-gobot")
			req.Header.Set("X-Title", "ok-gobot")
		}

		// Use a client without timeout for streaming
		streamClient := &http.Client{}
		resp, err := streamClient.Do(req)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("request failed: %w", err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			ch <- StreamChunk{Error: fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))}
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Error: ctx.Err(), Done: true}
				return
			default:
			}

			line := scanner.Text()
			if line == "" {
				continue
			}

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				ch <- StreamChunk{Done: true}
				return
			}

			var chunk streamChunkResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			if len(chunk.Choices) > 0 {
				choice := chunk.Choices[0]
				ch <- StreamChunk{
					Content:      choice.Delta.Content,
					FinishReason: choice.FinishReason,
					Done:         choice.FinishReason != "",
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("stream read error: %w", err)}
		}
	}()

	return ch
}

// CompleteStreamWithTools sends messages with tool definitions and returns a channel of streamed chunks
// This supports streaming responses that may include tool calls
func (c *OpenAICompatibleClient) CompleteStreamWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) <-chan StreamChunk {
	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)

		reqBody := ChatCompletionRequest{
			Model:    c.config.Model,
			Messages: messages,
			Tools:    tools,
			Stream:   true,
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		req, err := http.NewRequestWithContext(
			ctx,
			"POST",
			c.config.BaseURL+"/chat/completions",
			bytes.NewBuffer(jsonData),
		)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to create request: %w", err)}
			return
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
		req.Header.Set("Accept", "text/event-stream")

		if c.config.Name == "openrouter" {
			req.Header.Set("HTTP-Referer", "https://github.com/BeFeast/ok-gobot")
			req.Header.Set("X-Title", "ok-gobot")
		}

		// Use a client without timeout for streaming
		streamClient := &http.Client{}
		resp, err := streamClient.Do(req)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("request failed: %w", err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			ch <- StreamChunk{Error: fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))}
			return
		}

		// Track tool calls being built incrementally
		toolCallsMap := make(map[int]*ToolCall)
		var contentBuilder strings.Builder

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Error: ctx.Err(), Done: true}
				return
			default:
			}

			line := scanner.Text()
			if line == "" {
				continue
			}

			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				// Send final chunk with accumulated tool calls if any
				if len(toolCallsMap) > 0 {
					var toolCalls []ToolCall
					for i := 0; i < len(toolCallsMap); i++ {
						if tc, ok := toolCallsMap[i]; ok {
							toolCalls = append(toolCalls, *tc)
						}
					}
					// Encode tool calls as special marker in content for backward compatibility
					toolCallsJSON, _ := json.Marshal(toolCalls)
					ch <- StreamChunk{
						Content: "\n__TOOL_CALLS__:" + string(toolCallsJSON),
						Done:    true,
					}
				} else {
					ch <- StreamChunk{Done: true}
				}
				return
			}

			var chunk StreamChunkResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			if len(chunk.Choices) > 0 {
				choice := chunk.Choices[0]
				delta := choice.Delta

				// Handle content
				if delta.Content != "" {
					contentBuilder.WriteString(delta.Content)
					ch <- StreamChunk{
						Content:      delta.Content,
						FinishReason: choice.FinishReason,
						Done:         false,
					}
				}

				// Handle tool calls (they come incrementally)
				if len(delta.ToolCalls) > 0 {
					for _, tc := range delta.ToolCalls {
						// Tool calls have an index to identify which call is being updated
						idx := 0 // Default index
						// OpenAI includes index in the tool call during streaming
						// For simplicity, we'll use the order they appear

						if _, exists := toolCallsMap[idx]; !exists {
							toolCallsMap[idx] = &ToolCall{
								ID:   tc.ID,
								Type: tc.Type,
								Function: FunctionCall{
									Name:      tc.Function.Name,
									Arguments: tc.Function.Arguments,
								},
							}
						} else {
							// Append to existing tool call
							if tc.ID != "" {
								toolCallsMap[idx].ID = tc.ID
							}
							if tc.Type != "" {
								toolCallsMap[idx].Type = tc.Type
							}
							if tc.Function.Name != "" {
								toolCallsMap[idx].Function.Name = tc.Function.Name
							}
							if tc.Function.Arguments != "" {
								toolCallsMap[idx].Function.Arguments += tc.Function.Arguments
							}
						}
					}
				}

				// Send finish chunk
				if choice.FinishReason != "" {
					ch <- StreamChunk{
						FinishReason: choice.FinishReason,
						Done:         true,
					}
				}
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("stream read error: %w", err)}
		}
	}()

	return ch
}

// AvailableModels returns common models for each provider
func AvailableModels() map[string][]string {
	return map[string][]string{
		"openrouter": {
			"moonshotai/kimi-k2.5",          // Kimi K2.5
			"anthropic/claude-3.5-sonnet",   // Claude 3.5 Sonnet
			"anthropic/claude-3-opus",       // Claude 3 Opus
			"openai/gpt-4o",                 // GPT-4o
			"openai/gpt-4o-mini",            // GPT-4o Mini
			"google/gemini-pro-1.5",         // Gemini Pro 1.5
			"meta-llama/llama-3.1-70b",      // Llama 3.1 70B
			"mistralai/mistral-large",       // Mistral Large
			"nvidia/llama-3.1-nemotron-70b", // Nemotron 70B
		},
		"openai": {
			"gpt-4o",
			"gpt-4o-mini",
			"gpt-4-turbo",
			"gpt-3.5-turbo",
		},
	}
}

// ConvertLegacyMessages converts old Message type to new ChatMessage type
func ConvertLegacyMessages(messages []Message) []ChatMessage {
	result := make([]ChatMessage, len(messages))
	for i, msg := range messages {
		result[i] = ChatMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}
	}
	return result
}
