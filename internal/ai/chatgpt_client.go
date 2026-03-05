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

const (
	defaultChatGPTBaseURL = "https://chatgpt.com/backend-api"
	chatGPTCodexPath      = "/codex/responses"
)

// ChatGPTClient implements Client and StreamingClient for ChatGPT's Codex Responses API.
// This uses the chatgpt.com/backend-api/codex/responses endpoint which follows the
// OpenAI Responses API SSE format, authenticated via ChatGPT Pro OAuth JWT token.
type ChatGPTClient struct {
	config     ProviderConfig
	httpClient *http.Client
}

// SupportsVision reports whether this client currently accepts multimodal user blocks.
func (c *ChatGPTClient) SupportsVision() bool {
	return true
}

// NewChatGPTClient creates a new ChatGPT Codex Responses API client.
func NewChatGPTClient(config ProviderConfig) *ChatGPTClient {
	if config.BaseURL == "" {
		config.BaseURL = defaultChatGPTBaseURL
	}
	if config.Model == "" {
		config.Model = "gpt-5.4"
	}
	return &ChatGPTClient{
		config: config,
		httpClient: &http.Client{
			Timeout: 180 * time.Second,
		},
	}
}

// chatGPTRequest represents the request body for the Codex Responses API.
type chatGPTRequest struct {
	Model        string               `json:"model"`
	Instructions string               `json:"instructions"`
	Input        []chatGPTInputMsg    `json:"input"`
	Stream       bool                 `json:"stream"`
	Store        bool                 `json:"store"`
	Tools        []chatGPTToolDef     `json:"tools,omitempty"`
}

// chatGPTInputMsg represents an input message in the Responses API format.
type chatGPTInputMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatGPTToolDef represents a tool definition for the Codex API.
type chatGPTToolDef struct {
	Type     string                 `json:"type"`
	Function chatGPTFunctionDef     `json:"function,omitempty"`
	Name     string                 `json:"name,omitempty"`
}

// chatGPTFunctionDef describes a function tool.
type chatGPTFunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// chatGPTSSEEvent represents a parsed SSE event from the Codex API.
type chatGPTSSEEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"-"`
}

// chatGPTResponseCompleted is the response.completed event payload.
type chatGPTResponseCompleted struct {
	Type     string `json:"type"`
	Response struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Model  string `json:"model"`
		Output []struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Status  string `json:"status"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

// chatGPTTextDelta is the response.output_text.delta event payload.
type chatGPTTextDelta struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta"`
	OutputIndex  int    `json:"output_index"`
}

// chatGPTErrorResponse represents an API error.
type chatGPTErrorResponse struct {
	Detail string `json:"detail"`
	Error  *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// buildRequest creates an HTTP request for the Codex Responses API.
func (c *ChatGPTClient) buildRequest(ctx context.Context, body []byte) (*http.Request, error) {
	url := strings.TrimRight(c.config.BaseURL, "/") + chatGPTCodexPath

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Accept", "text/event-stream")
	// Browser-like headers to pass Cloudflare
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/")

	return req, nil
}

// convertMessages converts ok-gobot Message types to Codex API input format.
// The first system message becomes the "instructions" field.
func (c *ChatGPTClient) convertMessages(messages []Message) (string, []chatGPTInputMsg) {
	instructions := "You are a helpful assistant."
	var input []chatGPTInputMsg

	for _, msg := range messages {
		if msg.Role == "system" {
			instructions = msg.Content
			continue
		}
		input = append(input, chatGPTInputMsg{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	return instructions, input
}

// convertChatMessages converts ChatMessage types to Codex API input format.
func (c *ChatGPTClient) convertChatMessages(messages []ChatMessage) (string, []chatGPTInputMsg) {
	instructions := "You are a helpful assistant."
	var input []chatGPTInputMsg

	for _, msg := range messages {
		if msg.Role == "system" {
			instructions = msg.Content
			continue
		}
		// Skip tool result messages for now (Codex handles tools differently)
		if msg.Role == "tool" {
			continue
		}
		input = append(input, chatGPTInputMsg{
			Role:    msg.Role,
			Content: msg.Content,
		})
	}

	return instructions, input
}

// Complete sends messages and returns the full response (non-streaming).
func (c *ChatGPTClient) Complete(ctx context.Context, messages []Message) (string, error) {
	logger.Debugf("ChatGPT Complete: model=%s messages=%d", c.config.Model, len(messages))

	instructions, input := c.convertMessages(messages)

	reqBody := chatGPTRequest{
		Model:        c.config.Model,
		Instructions: instructions,
		Input:        input,
		Stream:       true, // API requires stream:true always
		Store:        false,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := c.buildRequest(ctx, jsonData)
	if err != nil {
		return "", err
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
		var errResp chatGPTErrorResponse
		if json.Unmarshal(body, &errResp) == nil {
			if errResp.Detail != "" {
				return "", fmt.Errorf("ChatGPT API error (status %d): %s", resp.StatusCode, errResp.Detail)
			}
			if errResp.Error != nil {
				return "", fmt.Errorf("ChatGPT API error (status %d): %s", resp.StatusCode, errResp.Error.Message)
			}
		}
		return "", fmt.Errorf("ChatGPT API error (status %d): %s", resp.StatusCode, string(body))
	}

	// API returns SSE stream even for Complete — collect all text delta chunks
	var text strings.Builder
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var evt map[string]interface{}
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}
		if evtType, _ := evt["type"].(string); evtType == "response.output_text.delta" {
			if delta, ok := evt["delta"].(string); ok {
				text.WriteString(delta)
			}
		}
	}

	content := text.String()
	logger.Debugf("ChatGPT Complete response: len=%d", len(content))
	return content, nil
}

// CompleteWithTools sends messages with tool definitions and returns the full response.
func (c *ChatGPTClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	logger.Debugf("ChatGPT CompleteWithTools: model=%s messages=%d tools=%d", c.config.Model, len(messages), len(tools))

	instructions, input := c.convertChatMessages(messages)

	// Convert tool definitions to Codex format
	var codexTools []chatGPTToolDef
	for _, tool := range tools {
		codexTools = append(codexTools, chatGPTToolDef{
			Type: "function",
			Function: chatGPTFunctionDef{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			},
		})
	}

	reqBody := chatGPTRequest{
		Model:        c.config.Model,
		Instructions: instructions,
		Input:        input,
		Stream:       true, // Always stream for tool calls to parse SSE
		Store:        false,
		Tools:        codexTools,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := c.buildRequest(ctx, jsonData)
	if err != nil {
		return nil, err
	}

	streamClient := &http.Client{}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ChatGPT API error (status %d): %s", resp.StatusCode, string(body))
	}

	// Parse SSE stream to collect the full response
	var fullText strings.Builder
	var toolCalls []ToolCall

	scanner := bufio.NewScanner(resp.Body)
	// Increase buffer size for large SSE events
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		switch event.Type {
		case "response.output_text.delta":
			var delta chatGPTTextDelta
			if err := json.Unmarshal([]byte(data), &delta); err == nil {
				fullText.WriteString(delta.Delta)
			}
		case "response.completed":
			var completed chatGPTResponseCompleted
			if err := json.Unmarshal([]byte(data), &completed); err == nil {
				// Extract any function calls from output
				for _, item := range completed.Response.Output {
					if item.Type == "function_call" {
						// Parse function call from the output
						var fc struct {
							ID        string `json:"id"`
							CallID    string `json:"call_id"`
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}
						rawJSON, _ := json.Marshal(item)
						if json.Unmarshal(rawJSON, &fc) == nil && fc.Name != "" {
							toolCalls = append(toolCalls, ToolCall{
								ID:   fc.CallID,
								Type: "function",
								Function: FunctionCall{
									Name:      fc.Name,
									Arguments: fc.Arguments,
								},
							})
						}
					}
				}
			}
		}
	}

	// Build ChatCompletionResponse
	result := &ChatCompletionResponse{
		Model: c.config.Model,
		Choices: []struct {
			Index        int         `json:"index"`
			Message      ChatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: ChatMessage{
					Role:      "assistant",
					Content:   fullText.String(),
					ToolCalls: toolCalls,
				},
				FinishReason: func() string {
					if len(toolCalls) > 0 {
						return "tool_calls"
					}
					return "stop"
				}(),
			},
		},
	}

	logger.Debugf("ChatGPT CompleteWithTools response: content_len=%d tool_calls=%d", fullText.Len(), len(toolCalls))
	return result, nil
}

// CompleteStream sends messages and returns a channel of streamed chunks.
func (c *ChatGPTClient) CompleteStream(ctx context.Context, messages []Message) <-chan StreamChunk {
	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)

		instructions, input := c.convertMessages(messages)

		reqBody := chatGPTRequest{
			Model:        c.config.Model,
			Instructions: instructions,
			Input:        input,
			Stream:       true,
			Store:        false,
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		req, err := c.buildRequest(ctx, jsonData)
		if err != nil {
			ch <- StreamChunk{Error: err}
			return
		}

		streamClient := &http.Client{}
		resp, err := streamClient.Do(req)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("request failed: %w", err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			ch <- StreamChunk{Error: fmt.Errorf("ChatGPT API error (status %d): %s", resp.StatusCode, string(body))}
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Error: ctx.Err(), Done: true}
				return
			default:
			}

			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				ch <- StreamChunk{Done: true}
				return
			}

			var event struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "response.output_text.delta":
				var delta chatGPTTextDelta
				if err := json.Unmarshal([]byte(data), &delta); err == nil {
					ch <- StreamChunk{
						Content: delta.Delta,
						Done:    false,
					}
				}
			case "response.completed":
				ch <- StreamChunk{
					Done:         true,
					FinishReason: "stop",
				}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("stream read error: %w", err)}
		}
	}()

	return ch
}

// CompleteStreamWithTools sends messages with tool definitions and streams chunks.
func (c *ChatGPTClient) CompleteStreamWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) <-chan StreamChunk {
	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)

		instructions, input := c.convertChatMessages(messages)

		var codexTools []chatGPTToolDef
		for _, tool := range tools {
			codexTools = append(codexTools, chatGPTToolDef{
				Type: "function",
				Function: chatGPTFunctionDef{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					Parameters:  tool.Function.Parameters,
				},
			})
		}

		reqBody := chatGPTRequest{
			Model:        c.config.Model,
			Instructions: instructions,
			Input:        input,
			Stream:       true,
			Store:        false,
			Tools:        codexTools,
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		req, err := c.buildRequest(ctx, jsonData)
		if err != nil {
			ch <- StreamChunk{Error: err}
			return
		}

		streamClient := &http.Client{}
		resp, err := streamClient.Do(req)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("request failed: %w", err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			ch <- StreamChunk{Error: fmt.Errorf("ChatGPT API error (status %d): %s", resp.StatusCode, string(body))}
			return
		}

		var toolCallsMap = make(map[int]*ToolCall)

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Error: ctx.Err(), Done: true}
				return
			default:
			}

			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var event struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "response.output_text.delta":
				var delta chatGPTTextDelta
				if err := json.Unmarshal([]byte(data), &delta); err == nil {
					ch <- StreamChunk{
						Content: delta.Delta,
						Done:    false,
					}
				}
			case "response.function_call_arguments.delta":
				// Function call argument streaming — accumulate
				var fcDelta struct {
					OutputIndex int    `json:"output_index"`
					Delta       string `json:"delta"`
				}
				if err := json.Unmarshal([]byte(data), &fcDelta); err == nil {
					if tc, ok := toolCallsMap[fcDelta.OutputIndex]; ok {
						tc.Function.Arguments += fcDelta.Delta
					}
				}
			case "response.output_item.added":
				// New output item — could be a function call
				var item struct {
					OutputIndex int `json:"output_index"`
					Item        struct {
						ID     string `json:"id"`
						CallID string `json:"call_id"`
						Type   string `json:"type"`
						Name   string `json:"name"`
					} `json:"item"`
				}
				if err := json.Unmarshal([]byte(data), &item); err == nil {
					if item.Item.Type == "function_call" {
						toolCallsMap[item.OutputIndex] = &ToolCall{
							ID:   item.Item.CallID,
							Type: "function",
							Function: FunctionCall{
								Name:      item.Item.Name,
								Arguments: "",
							},
						}
					}
				}
			case "response.completed":
				// Send final chunk with accumulated tool calls if any
				if len(toolCallsMap) > 0 {
					var toolCalls []ToolCall
					for i := 0; i < len(toolCallsMap); i++ {
						if tc, ok := toolCallsMap[i]; ok {
							toolCalls = append(toolCalls, *tc)
						}
					}
					toolCallsJSON, _ := json.Marshal(toolCalls)
					ch <- StreamChunk{
						Content: "\n__TOOL_CALLS__:" + string(toolCallsJSON),
						Done:    true,
					}
				} else {
					ch <- StreamChunk{
						Done:         true,
						FinishReason: "stop",
					}
				}
				return
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("stream read error: %w", err)}
		}
	}()

	return ch
}
