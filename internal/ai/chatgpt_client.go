package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"ok-gobot/internal/logger"
)

const (
	defaultChatGPTBaseURL = "https://chatgpt.com/backend-api"
	chatGPTCodexPath      = "/codex/responses"
)

// Terminal stream conditions shared by the buffered and streaming parsers.
// Both paths must report them the same way: a caller that receives neither
// text nor tool calls has no way to tell a broken stream from a model that
// legitimately chose to say nothing.
var (
	errChatGPTNoUsableOutput   = errors.New("ChatGPT API returned no usable text or tool calls")
	errChatGPTStreamEndedEarly = errors.New("ChatGPT API stream ended without a completed response")
)

// ChatGPTClient implements Client and StreamingClient for ChatGPT's Codex Responses API.
// This uses the chatgpt.com/backend-api/codex/responses endpoint which follows the
// OpenAI Responses API SSE format, authenticated from the Codex-owned auth cache.
type ChatGPTClient struct {
	config       ProviderConfig
	httpClient   *http.Client
	streamClient *http.Client
	auth         *chatGPTAuthManager
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
		config.Model = "gpt-5.6-sol"
	}
	return &ChatGPTClient{
		config: config,
		httpClient: &http.Client{
			Timeout: 180 * time.Second,
		},
		streamClient: &http.Client{},
		auth:         newChatGPTAuthManager(config),
	}
}

// chatGPTRequest represents the request body for the Codex Responses API.
type chatGPTRequest struct {
	Model        string             `json:"model"`
	Instructions string             `json:"instructions"`
	Input        []chatGPTInputItem `json:"input"`
	Stream       bool               `json:"stream"`
	Store        bool               `json:"store"`
	Reasoning    *chatGPTReasoning  `json:"reasoning,omitempty"`
	Tools        []chatGPTToolDef   `json:"tools,omitempty"`
}

type chatGPTReasoning struct {
	Effort string `json:"effort"`
}

func (c *ChatGPTClient) reasoning() *chatGPTReasoning {
	effort := strings.ToLower(strings.TrimSpace(c.config.ThinkLevel))
	if effort == "off" {
		effort = "none"
	}
	switch effort {
	case "none", "low", "medium", "high", "xhigh", "max":
		return &chatGPTReasoning{Effort: effort}
	default:
		return nil
	}
}

// chatGPTInputItem represents an input item in the Responses API format.
// Items can be messages (role+content), function_call, or function_call_output.
type chatGPTInputItem map[string]interface{}

// chatGPTToolDef represents a tool definition for the Codex Responses API.
// The Responses API uses a flat format: {type, name, description, parameters}
// NOT the nested Chat Completions format: {type, function: {name, ...}}
type chatGPTToolDef struct {
	Type        string          `json:"type"`
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
			ID        string `json:"id"`
			Type      string `json:"type"`
			Status    string `json:"status"`
			Role      string `json:"role"`
			CallID    string `json:"call_id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
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

// chatGPTResponseTerminalError is the shared envelope for terminal failure
// events emitted by the Responses API.
type chatGPTResponseTerminalError struct {
	Type     string `json:"type"`
	Response struct {
		Status string `json:"status"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

// chatGPTStreamErrorEvent decodes the top-level `error` SSE event, which is
// shaped differently from response.failed (no `response` envelope).
func chatGPTStreamErrorEvent(data []byte) error {
	var event struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("ChatGPT API error event could not be decoded: %w", err)
	}
	detail := strings.TrimSpace(event.Message)
	if code := strings.TrimSpace(event.Code); code != "" {
		if detail != "" {
			detail = code + ": " + detail
		} else {
			detail = code
		}
	}
	if detail == "" {
		return errors.New("ChatGPT API error event")
	}
	return fmt.Errorf("ChatGPT API error event: %s", detail)
}

func chatGPTTerminalError(data []byte, eventType string) error {
	var event chatGPTResponseTerminalError
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("ChatGPT API %s event could not be decoded: %w", eventType, err)
	}

	switch eventType {
	case "response.failed":
		if event.Response.Error != nil {
			detail := strings.TrimSpace(event.Response.Error.Message)
			if code := strings.TrimSpace(event.Response.Error.Code); code != "" {
				if detail != "" {
					detail = code + ": " + detail
				} else {
					detail = code
				}
			}
			if detail != "" {
				return fmt.Errorf("ChatGPT API response failed: %s", detail)
			}
		}
		return fmt.Errorf("ChatGPT API response failed")
	case "response.incomplete":
		if event.Response.IncompleteDetails != nil {
			if reason := strings.TrimSpace(event.Response.IncompleteDetails.Reason); reason != "" {
				return fmt.Errorf("ChatGPT API response incomplete: %s", reason)
			}
		}
		return fmt.Errorf("ChatGPT API response incomplete")
	default:
		return fmt.Errorf("ChatGPT API terminal error: %s", eventType)
	}
}

func completedChatGPTText(event chatGPTResponseCompleted) string {
	var text strings.Builder
	for _, item := range event.Response.Output {
		for _, content := range item.Content {
			if content.Type == "output_text" {
				text.WriteString(content.Text)
			}
		}
	}
	return text.String()
}

func completedChatGPTToolCalls(event chatGPTResponseCompleted) []ToolCall {
	toolCalls := make([]ToolCall, 0)
	for _, item := range event.Response.Output {
		if item.Type != "function_call" || item.Name == "" {
			continue
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:   item.CallID,
			Type: "function",
			Function: FunctionCall{
				Name:      item.Name,
				Arguments: item.Arguments,
			},
		})
	}
	return toolCalls
}

func orderedChatGPTToolCalls(toolCallsMap map[int]*ToolCall) []ToolCall {
	indexes := make([]int, 0, len(toolCallsMap))
	for index := range toolCallsMap {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	toolCalls := make([]ToolCall, 0, len(indexes))
	for _, index := range indexes {
		if toolCall := toolCallsMap[index]; toolCall != nil {
			toolCalls = append(toolCalls, *toolCall)
		}
	}
	return toolCalls
}

// chatGPTTextDelta is the response.output_text.delta event payload.
type chatGPTTextDelta struct {
	Type         string `json:"type"`
	ContentIndex int    `json:"content_index"`
	Delta        string `json:"delta"`
	OutputIndex  int    `json:"output_index"`
}

// buildRequest creates an HTTP request for the Codex Responses API.
func (c *ChatGPTClient) buildRequest(ctx context.Context, body []byte, creds chatGPTCredentials) (*http.Request, error) {
	url := strings.TrimRight(c.config.BaseURL, "/") + chatGPTCodexPath

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+creds.accessToken)
	if creds.accountID != "" {
		req.Header.Set("ChatGPT-Account-ID", creds.accountID)
	}
	req.Header.Set("Accept", "text/event-stream")
	// Browser-like headers to pass Cloudflare
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Referer", "https://chatgpt.com/")

	return req, nil
}

// convertMessages converts ok-gobot Message types to Codex API input format.
// The first system message becomes the "instructions" field.
func (c *ChatGPTClient) convertMessages(messages []Message) (string, []chatGPTInputItem) {
	instructions := "You are a helpful assistant."
	var input []chatGPTInputItem

	for _, msg := range messages {
		if msg.Role == "system" {
			instructions = msg.Content
			continue
		}
		input = append(input, chatGPTInputItem{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	return instructions, input
}

// convertChatMessages converts ChatMessage types to Codex Responses API input format.
// The Responses API uses a different format than Chat Completions:
//   - assistant messages with tool calls become one or more function_call items
//   - tool result messages become function_call_output items
//   - regular messages stay as role+content items
func (c *ChatGPTClient) convertChatMessages(messages []ChatMessage) (string, []chatGPTInputItem) {
	instructions := "You are a helpful assistant."
	var input []chatGPTInputItem

	for _, msg := range messages {
		if msg.Role == "system" {
			instructions = msg.Content
			continue
		}

		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// Emit text content as a message if present
			if msg.Content != "" {
				input = append(input, chatGPTInputItem{
					"type":    "message",
					"role":    "assistant",
					"content": msg.Content,
				})
			}
			// Emit each tool call as a function_call item
			for _, tc := range msg.ToolCalls {
				input = append(input, chatGPTInputItem{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
			continue
		}

		if msg.Role == "tool" {
			// Tool results become function_call_output items
			input = append(input, chatGPTInputItem{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
				"output":  msg.Content,
			})
			continue
		}

		// Multimodal user messages: serialize text + image blocks as
		// Responses content parts. Without this the images are silently
		// dropped and the model only ever sees the text placeholder.
		if msg.Role == RoleUser && len(msg.ContentBlocks) > 0 {
			if parts := chatGPTContentParts(msg); len(parts) > 0 {
				input = append(input, chatGPTInputItem{
					"role":    msg.Role,
					"content": parts,
				})
				continue
			}
		}

		input = append(input, chatGPTInputItem{
			"role":    msg.Role,
			"content": msg.Content,
		})
	}

	return instructions, input
}

// chatGPTContentParts converts a multimodal ChatMessage into Codex Responses
// content parts: input_text for the text, input_image with a base64 data URL
// for each image block. When the blocks already carry text (the caption),
// msg.Content is skipped so the caption is not sent twice.
func chatGPTContentParts(msg ChatMessage) []map[string]interface{} {
	hasTextBlock := false
	for _, block := range msg.ContentBlocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			hasTextBlock = true
			break
		}
	}
	parts := make([]map[string]interface{}, 0, len(msg.ContentBlocks)+1)
	if !hasTextBlock && strings.TrimSpace(msg.Content) != "" {
		parts = append(parts, map[string]interface{}{"type": "input_text", "text": msg.Content})
	}
	for _, block := range msg.ContentBlocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, map[string]interface{}{"type": "input_text", "text": block.Text})
			}
		case "image":
			if block.Source != nil && block.Source.Data != "" {
				parts = append(parts, map[string]interface{}{
					"type":      "input_image",
					"image_url": "data:" + block.Source.MediaType + ";base64," + block.Source.Data,
				})
			}
		}
	}
	return parts
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
		Reasoning:    c.reasoning(),
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.doRequest(ctx, jsonData, c.httpClient)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", &BackendHTTPError{Provider: "ChatGPT", StatusCode: resp.StatusCode}
	}

	// API returns SSE stream even for Complete — collect all text delta chunks
	var text strings.Builder
	var completedText string
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
		} else if evtType == "response.completed" {
			var completed chatGPTResponseCompleted
			if err := json.Unmarshal([]byte(data), &completed); err == nil {
				completedText = completedChatGPTText(completed)
			}
		}
	}

	if text.Len() == 0 {
		text.WriteString(completedText)
	}
	content := text.String()
	logger.Debugf("ChatGPT Complete response: len=%d", len(content))
	return content, nil
}

// CompleteWithTools sends messages with tool definitions and returns the full response.
func (c *ChatGPTClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	logger.Debugf("ChatGPT CompleteWithTools: model=%s messages=%d tools=%d", c.config.Model, len(messages), len(tools))

	instructions, input := c.convertChatMessages(messages)

	// Convert tool definitions to Codex Responses API format (flat, not nested)
	var codexTools []chatGPTToolDef
	for _, tool := range tools {
		codexTools = append(codexTools, chatGPTToolDef{
			Type:        "function",
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  tool.Function.Parameters,
		})
	}

	reqBody := chatGPTRequest{
		Model:        c.config.Model,
		Instructions: instructions,
		Input:        input,
		Stream:       true, // Always stream for tool calls to parse SSE
		Store:        false,
		Reasoning:    c.reasoning(),
		Tools:        codexTools,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.doRequest(ctx, jsonData, c.streamClient)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &BackendHTTPError{Provider: "ChatGPT", StatusCode: resp.StatusCode}
	}

	// Parse SSE stream to collect the full response
	var fullText strings.Builder
	var toolCalls []ToolCall
	toolCallsMap := make(map[int]*ToolCall)
	var terminalErr error

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
		case "response.output_item.added":
			var item struct {
				OutputIndex int `json:"output_index"`
				Item        struct {
					CallID string `json:"call_id"`
					Type   string `json:"type"`
					Name   string `json:"name"`
				} `json:"item"`
			}
			if err := json.Unmarshal([]byte(data), &item); err == nil && item.Item.Type == "function_call" {
				toolCallsMap[item.OutputIndex] = &ToolCall{
					ID:   item.Item.CallID,
					Type: "function",
					Function: FunctionCall{
						Name: item.Item.Name,
					},
				}
			}
		case "response.function_call_arguments.delta":
			var delta struct {
				OutputIndex int    `json:"output_index"`
				Delta       string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &delta); err == nil {
				if toolCall := toolCallsMap[delta.OutputIndex]; toolCall != nil {
					toolCall.Function.Arguments += delta.Delta
				}
			}
		case "response.function_call_arguments.done":
			var done struct {
				OutputIndex int    `json:"output_index"`
				Arguments   string `json:"arguments"`
			}
			if err := json.Unmarshal([]byte(data), &done); err == nil && done.Arguments != "" {
				if toolCall := toolCallsMap[done.OutputIndex]; toolCall != nil {
					toolCall.Function.Arguments = done.Arguments
				}
			}
		case "response.output_item.done":
			var done struct {
				OutputIndex int `json:"output_index"`
				Item        struct {
					CallID    string `json:"call_id"`
					Type      string `json:"type"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"item"`
			}
			if err := json.Unmarshal([]byte(data), &done); err == nil && done.Item.Type == "function_call" {
				toolCall := toolCallsMap[done.OutputIndex]
				if toolCall == nil {
					toolCall = &ToolCall{Type: "function"}
					toolCallsMap[done.OutputIndex] = toolCall
				}
				if done.Item.CallID != "" {
					toolCall.ID = done.Item.CallID
				}
				if done.Item.Name != "" {
					toolCall.Function.Name = done.Item.Name
				}
				if done.Item.Arguments != "" {
					toolCall.Function.Arguments = done.Item.Arguments
				}
			}
		case "response.completed":
			var completed chatGPTResponseCompleted
			if err := json.Unmarshal([]byte(data), &completed); err == nil {
				if fullText.Len() == 0 {
					fullText.WriteString(completedChatGPTText(completed))
				}
				if completedToolCalls := completedChatGPTToolCalls(completed); len(completedToolCalls) > 0 {
					toolCalls = completedToolCalls
				} else {
					toolCalls = orderedChatGPTToolCalls(toolCallsMap)
				}
			}
		case "response.failed", "response.incomplete":
			terminalErr = chatGPTTerminalError([]byte(data), event.Type)
		}
		if terminalErr != nil {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ChatGPT API stream read error: %w", err)
	}
	if terminalErr != nil {
		return nil, terminalErr
	}
	if len(toolCalls) == 0 {
		toolCalls = orderedChatGPTToolCalls(toolCallsMap)
	}
	if strings.TrimSpace(fullText.String()) == "" && len(toolCalls) == 0 {
		return nil, errChatGPTNoUsableOutput
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
			Reasoning:    c.reasoning(),
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		resp, err := c.doRequest(ctx, jsonData, c.streamClient)
		if err != nil {
			ch <- StreamChunk{Error: err}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			ch <- StreamChunk{Error: &BackendHTTPError{Provider: "ChatGPT", StatusCode: resp.StatusCode}}
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		streamedText := false
		var terminalErr error

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
					if delta.Delta != "" {
						streamedText = true
					}
					ch <- StreamChunk{
						Content: delta.Delta,
						Done:    false,
					}
				}
			case "response.failed", "response.incomplete":
				terminalErr = chatGPTTerminalError([]byte(data), event.Type)
			case "response.completed":
				var completed chatGPTResponseCompleted
				fallbackText := ""
				if !streamedText && json.Unmarshal([]byte(data), &completed) == nil {
					fallbackText = completedChatGPTText(completed)
				}
				if !streamedText && strings.TrimSpace(fallbackText) == "" {
					ch <- StreamChunk{Error: errChatGPTNoUsableOutput}
					return
				}
				ch <- StreamChunk{
					Content:      fallbackText,
					Done:         true,
					FinishReason: "stop",
				}
				return
			}

			if terminalErr != nil {
				break
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("stream read error: %w", err)}
			return
		}

		// Same rule as the tool-calling stream: never close the channel silently.
		if streamedText {
			reason := "stream ended before completion"
			if terminalErr != nil {
				reason = terminalErr.Error()
			}
			logger.Warnf("ChatGPT stream truncated after partial text: %s", reason)
			ch <- StreamChunk{Done: true, FinishReason: "incomplete"}
			return
		}
		if terminalErr != nil {
			ch <- StreamChunk{Error: terminalErr}
			return
		}
		ch <- StreamChunk{Error: errChatGPTStreamEndedEarly}
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
				Type:        "function",
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			})
		}

		reqBody := chatGPTRequest{
			Model:        c.config.Model,
			Instructions: instructions,
			Input:        input,
			Stream:       true,
			Store:        false,
			Reasoning:    c.reasoning(),
			Tools:        codexTools,
		}

		jsonData, err := json.Marshal(reqBody)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to marshal request: %w", err)}
			return
		}

		resp, err := c.doRequest(ctx, jsonData, c.streamClient)
		if err != nil {
			ch <- StreamChunk{Error: err}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			ch <- StreamChunk{Error: &BackendHTTPError{Provider: "ChatGPT", StatusCode: resp.StatusCode}}
			return
		}

		var toolCallsMap = make(map[int]*ToolCall)

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		streamedText := false
		var terminalErr error
		// Counted so a silent stream can say what it did see. Without this the
		// three ways to end up with no answer leave byte-identical traces.
		eventsSeen, unhandledEvents := 0, 0
		lastEventType := ""

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
				lastEventType = "[DONE]"
				break
			}

			var event struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}
			eventsSeen++
			lastEventType = event.Type

			switch event.Type {
			case "response.output_text.delta":
				var delta chatGPTTextDelta
				if err := json.Unmarshal([]byte(data), &delta); err == nil {
					if delta.Delta != "" {
						streamedText = true
					}
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
			case "response.function_call_arguments.done":
				var done struct {
					OutputIndex int    `json:"output_index"`
					Arguments   string `json:"arguments"`
				}
				if err := json.Unmarshal([]byte(data), &done); err == nil && done.Arguments != "" {
					if tc := toolCallsMap[done.OutputIndex]; tc != nil {
						tc.Function.Arguments = done.Arguments
					}
				}
			case "response.output_item.done":
				var done struct {
					OutputIndex int `json:"output_index"`
					Item        struct {
						CallID    string `json:"call_id"`
						Type      string `json:"type"`
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"item"`
				}
				if err := json.Unmarshal([]byte(data), &done); err == nil && done.Item.Type == "function_call" {
					tc := toolCallsMap[done.OutputIndex]
					if tc == nil {
						tc = &ToolCall{Type: "function"}
						toolCallsMap[done.OutputIndex] = tc
					}
					if done.Item.CallID != "" {
						tc.ID = done.Item.CallID
					}
					if done.Item.Name != "" {
						tc.Function.Name = done.Item.Name
					}
					if done.Item.Arguments != "" {
						tc.Function.Arguments = done.Item.Arguments
					}
				}
			case "response.failed", "response.incomplete":
				terminalErr = chatGPTTerminalError([]byte(data), event.Type)
			case "error":
				terminalErr = chatGPTStreamErrorEvent([]byte(data))
			case "response.completed":
				var completed chatGPTResponseCompleted
				fallbackText := ""
				toolCalls := orderedChatGPTToolCalls(toolCallsMap)
				if json.Unmarshal([]byte(data), &completed) == nil {
					if !streamedText {
						fallbackText = completedChatGPTText(completed)
					}
					if completedToolCalls := completedChatGPTToolCalls(completed); len(completedToolCalls) > 0 {
						toolCalls = completedToolCalls
					}
				}

				if len(toolCalls) > 0 {
					toolCallsJSON, _ := json.Marshal(toolCalls)
					ch <- StreamChunk{
						Content: fallbackText + "\n__TOOL_CALLS__:" + string(toolCallsJSON),
						Done:    true,
					}
					return
				}
				if !streamedText && strings.TrimSpace(fallbackText) == "" {
					// A completed turn carrying neither text nor a tool call
					// (reasoning-only output). Report it rather than handing the
					// caller an empty answer it cannot tell apart from a real one.
					ch <- StreamChunk{Error: errChatGPTNoUsableOutput}
					return
				}
				ch <- StreamChunk{
					Content:      fallbackText,
					Done:         true,
					FinishReason: "stop",
				}
				return
			default:
				unhandledEvents++
			}

			if terminalErr != nil {
				break
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("stream read error: %w", err)}
			return
		}

		// Past this point the stream ended without response.completed. Closing the
		// channel here would look identical to a successful empty answer, so every
		// remaining path emits something, and says what it saw.
		trace := fmt.Sprintf("events=%d unhandled=%d last=%q", eventsSeen, unhandledEvents, lastEventType)
		if streamedText {
			// Text already reached the user; keep it rather than replacing a partial
			// answer with an error, but do not pretend the turn finished cleanly.
			reason := "ended before completion"
			if terminalErr != nil {
				reason = terminalErr.Error()
			}
			logger.Warnf("ChatGPT stream truncated after partial text: %s (%s)", reason, trace)
			ch <- StreamChunk{Done: true, FinishReason: "incomplete"}
			return
		}
		if terminalErr != nil {
			logger.Warnf("ChatGPT stream failed: %v (%s)", terminalErr, trace)
			ch <- StreamChunk{Error: terminalErr}
			return
		}
		logger.Warnf("ChatGPT stream produced no output (%s)", trace)
		ch <- StreamChunk{Error: fmt.Errorf("%w (%s)", errChatGPTStreamEndedEarly, trace)}
	}()

	return ch
}
