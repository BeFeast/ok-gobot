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
	// Images the model returned inside the message itself rather than through a
	// tool call. Telegram turns take the streaming path, so a chunk type that
	// cannot carry them means the picture is dropped even when the wire
	// delivered it.
	Images []InlineImage
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
	Name               string
	APIKey             string
	BaseURL            string
	Model              string
	ThinkLevel         string // "off", "low", "medium", "high", "adaptive" — used by Anthropic client
	ChatGPTAuthFile    string // Optional Codex-owned auth.json path for ChatGPT subscription auth.
	ChatGPTCodexHome   string // Optional CODEX_HOME; defaults to $CODEX_HOME or ~/.codex.
	ChatGPTCodexBinary string // Official Codex CLI used only to refresh its auth cache; defaults to "codex".
	// OAuthStorePath is used by providers with refreshable OAuth credentials (Anthropic).
	// Empty means provider defaults are used.
	OAuthStorePath string
}

// OpenAICompatibleClient implements Client for OpenAI-compatible APIs
// Works with: OpenAI, OpenRouter, Anyscale, Together, etc.
type OpenAICompatibleClient struct {
	config     ProviderConfig
	httpClient *http.Client
}

// SupportsVision reports whether this client currently accepts multimodal user blocks.
// OpenAI-compatible APIs (OpenAI, OpenRouter, etc.) support vision via the multimodal
// content array format, which is handled by ChatMessage.MarshalJSON.
func (c *OpenAICompatibleClient) SupportsVision() bool {
	return true
}

// NewClient creates a new AI client from provider configuration.
// Returns Client interface — use type assertion for streaming support.
func NewClient(config ProviderConfig) (Client, error) {
	return NewClientWithDroid(config, DroidConfig{})
}

// NewClientWithDroid creates a new AI client, accepting optional DroidConfig
// for the "droid" provider.
func NewClientWithDroid(config ProviderConfig, droidCfg DroidConfig) (Client, error) {
	if config.Name == "droid" {
		if config.Model == "" {
			config.Model = "glm-5"
		}
		return NewDroidClient(config, droidCfg), nil
	}

	if config.Name == "anthropic" {
		if config.BaseURL == "" {
			config.BaseURL = "https://api.anthropic.com"
		}
		if config.Model == "" {
			config.Model = "claude-sonnet-4-5-20250929"
		}
		return NewAnthropicClient(config), nil
	}

	// ChatGPT Codex Responses API (chatgpt.com/backend-api/codex/responses)
	if config.Name == "chatgpt" || config.Name == "openai-codex" {
		if config.BaseURL == "" {
			config.BaseURL = "https://chatgpt.com/backend-api"
		}
		if config.Model == "" {
			config.Model = "gpt-5.6-sol"
		}
		return NewChatGPTClient(config), nil
	}

	// OpenAI-compatible providers
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

	// Auto-detect Hermes models and use the native Hermes tool call parser.
	// This handles Ollama + hermes3 and any OpenAI-compatible endpoint serving
	// a NousResearch Hermes model (e.g. vLLM without --tool-call-parser hermes).
	if IsHermesModel(config.Model) {
		return newHermesClient(config), nil
	}

	return &OpenAICompatibleClient{
		config: config,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}, nil
}

const (
	// sseMaxLineBytes caps one server-sent-events line. The default
	// bufio.Scanner limit is 64 KiB, which is far too small here: a gateway
	// answering a tool-enabled request can deliver the whole response as a
	// single line — measured at 2.6 MB against the local gateway with a
	// realistic tool array, versus 427 bytes for the same request without
	// tools. Exceeding the limit fails the scan with "token too long", which
	// surfaces as every tool-using turn failing while plain chat still works.
	sseMaxLineBytes = 16 << 20
	// sseInitialBufferBytes is the starting allocation; the scanner grows up to
	// sseMaxLineBytes only when a line actually needs it.
	sseInitialBufferBytes = 64 << 10
)

// chatCompletionRequest represents the API request body (legacy)
type chatCompletionRequest struct {
	Model           string    `json:"model"`
	Messages        []Message `json:"messages"`
	Stream          bool      `json:"stream"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
}

// reasoningEffort maps the configured think level onto the chat-completions
// reasoning_effort parameter.
//
// The ChatGPT client sends the same intent as reasoning.effort on the Responses
// API; without this, moving a deployment from that client to an
// OpenAI-compatible endpoint silently dropped the configured thinking level and
// every turn ran at the provider default. That is invisible in logs and shows
// up only as worse answers.
//
// "off" has no representation here — the wire accepts low, medium, high, xhigh
// and max — so it omits the field and inherits the provider default rather than
// inventing a value.
func (c *OpenAICompatibleClient) reasoningEffort() string {
	switch level := strings.ToLower(strings.TrimSpace(c.config.ThinkLevel)); level {
	case "low", "medium", "high", "xhigh", "max":
		return level
	default:
		return ""
	}
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
		Model:           c.config.Model,
		Messages:        messages,
		Stream:          false,
		ReasoningEffort: c.reasoningEffort(),
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
		Model:           c.config.Model,
		Messages:        messages,
		Tools:           tools,
		Stream:          false,
		ReasoningEffort: c.reasoningEffort(),
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

	logger.Tracef("AI CompleteWithTools response body (%d bytes): %.3000s", len(body), string(body))

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
			Model:           c.config.Model,
			Messages:        messages,
			Stream:          true,
			ReasoningEffort: c.reasoningEffort(),
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
		scanner.Buffer(make([]byte, 0, sseInitialBufferBytes), sseMaxLineBytes)
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

// toolCallMarkerContent encodes the tool calls accumulated during a stream as
// the sentinel-prefixed content the agent loop parses. It reports false when
// no tool call was accumulated, so callers can emit a plain terminal chunk.
func toolCallMarkerContent(accumulated map[int]*ToolCall) (string, bool) {
	if len(accumulated) == 0 {
		return "", false
	}
	toolCalls := make([]ToolCall, 0, len(accumulated))
	for i := 0; i < len(accumulated); i++ {
		if tc, ok := accumulated[i]; ok {
			toolCalls = append(toolCalls, *tc)
		}
	}
	if len(toolCalls) == 0 {
		return "", false
	}
	// Encode tool calls as special marker in content for backward compatibility.
	toolCallsJSON, err := json.Marshal(toolCalls)
	if err != nil {
		return "", false
	}
	return "\n__TOOL_CALLS__:" + string(toolCallsJSON), true
}

// CompleteStreamWithTools sends messages with tool definitions and returns a channel of streamed chunks
// This supports streaming responses that may include tool calls
func (c *OpenAICompatibleClient) CompleteStreamWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) <-chan StreamChunk {
	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)

		reqBody := ChatCompletionRequest{
			Model:           c.config.Model,
			Messages:        messages,
			Tools:           tools,
			Stream:          true,
			ReasoningEffort: c.reasoningEffort(),
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
		lastToolCallIdx := 0
		var contentBuilder strings.Builder

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, sseInitialBufferBytes), sseMaxLineBytes)
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
				if marker, ok := toolCallMarkerContent(toolCallsMap); ok {
					ch <- StreamChunk{
						Content: marker,
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

				// Handle inline images. They arrive without any content beside
				// them, so this cannot be folded into the branch above.
				if len(delta.Images) > 0 {
					ch <- StreamChunk{
						Images:       delta.Images,
						FinishReason: choice.FinishReason,
						Done:         false,
					}
				}

				// Handle tool calls (they come incrementally)
				if len(delta.ToolCalls) > 0 {
					for _, tc := range delta.ToolCalls {
						// Parallel tool calls stream interleaved; the index says which
						// call a fragment belongs to. A fragment without an index but
						// with a fresh id opens the next call; anything else continues
						// the call we were on.
						idx := lastToolCallIdx
						switch {
						case tc.Index != nil:
							idx = *tc.Index
						case tc.ID != "":
							idx = len(toolCallsMap)
						}
						lastToolCallIdx = idx

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

				// Send finish chunk.
				//
				// A terminal finish_reason must never be announced while tool
				// calls are still buffered. The consumer stops reading at the
				// first Done chunk, so the marker built at [DONE] would never
				// be seen: the gateway ends a tool-calling turn with an empty
				// delta carrying finish_reason "tool_calls" and only then
				// sends [DONE]. Measured 2026-09-01 against the local gateway,
				// that ordering silently dropped every streamed tool call —
				// the turn came back with no text and no tool calls at all and
				// was reported to the user as "empty model output".
				if choice.FinishReason != "" {
					if marker, ok := toolCallMarkerContent(toolCallsMap); ok {
						ch <- StreamChunk{
							Content:      marker,
							FinishReason: choice.FinishReason,
							Done:         true,
						}
						return
					}
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
		"anthropic": {
			"claude-opus-4-5-20251101",
			"claude-sonnet-4-5-20250929",
			"claude-sonnet-4-20250514",
			"claude-haiku-3-5-20241022",
		},
		"droid": {
			"glm-5",        // GLM-5 (Zhipu AI)
			"kimi-k2.5",    // Kimi K2.5 (Moonshot)
			"minimax-m2.5", // MiniMax M2.5
			"glm-4.7",      // GLM-4.7
		},
		"chatgpt": {
			"gpt-5.6-sol",   // GPT-5.6 SOL (preferred ChatGPT agent model)
			"gpt-5.6-luna",  // GPT-5.6 LUNA (fast low-latency tier; interaction fast lane)
			"gpt-5.4",       // GPT-5.4 compatibility fallback
			"gpt-5.3-codex", // GPT-5.3 Codex
			"gpt-5.2-codex", // GPT-5.2 Codex
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
