package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"ok-gobot/internal/logger"
)

const anthropicDefaultMaxTokens = 4096
const claudeCodeIdentity = "You are Claude Code, Anthropic's official CLI for Claude."

const (
	anthropicVersionHeader        = "2023-06-01"
	anthropicThinkingBetaHeader   = "interleaved-thinking-2025-05-14" // gitleaks:allow // gitleaks:allow
	anthropicOAuthBetaHeader      = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14" // gitleaks:allow // gitleaks:allow
	anthropicSetupTokenBetaHeader = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14" // gitleaks:allow // gitleaks:allow
	anthropicOAuthUserAgent       = "claude-cli/2.1.2 (external, cli)"
)

// thinkingForLevel maps a ThinkLevel string to an Anthropic ThinkingConfig.
// Returns nil for "off" or empty (thinking disabled).
// For "adaptive", the model decides whether and how much to think;
// budget_tokens is treated as a maximum ceiling.
// For other providers that don't support native thinking, callers should
// fall back to "medium" or ignore the level entirely.
func thinkingForLevel(level string) *ThinkingConfig {
	switch level {
	case "low":
		return &ThinkingConfig{Type: "enabled", BudgetTokens: 1024}
	case "medium":
		return &ThinkingConfig{Type: "enabled", BudgetTokens: 8000}
	case "high":
		return &ThinkingConfig{Type: "enabled", BudgetTokens: 32000}
	case "adaptive":
		return &ThinkingConfig{Type: "adaptive", BudgetTokens: 8000}
	default: // "off" or ""
		return nil
	}
}

// maxTokensForThinking returns a max_tokens value that satisfies Anthropic's
// constraint: max_tokens must be greater than budget_tokens.
func maxTokensForThinking(thinking *ThinkingConfig, defaultMax int) int {
	if thinking == nil {
		return defaultMax
	}
	// Anthropic requires max_tokens > budget_tokens; add headroom for actual output.
	minRequired := thinking.BudgetTokens + 1024
	if defaultMax < minRequired {
		return minRequired
	}
	return defaultMax
}

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

// SupportsVision reports whether the configured Anthropic model supports image inputs.
func (c *AnthropicClient) SupportsVision() bool {
	return anthropicModelSupportsVision(c.config.Model)
}

// Complete sends messages and returns the text response.
func (c *AnthropicClient) Complete(ctx context.Context, messages []Message) (string, error) {
	logger.Debugf("Anthropic Complete: model=%s messages=%d", c.config.Model, len(messages))

	chatMessages := ConvertLegacyMessages(messages)
	system, anthropicMsgs := translateMessages(chatMessages)

	apiKey, err := c.resolveAPIKey(ctx)
	if err != nil {
		return "", err
	}

	thinking := thinkingForLevel(c.config.ThinkLevel)
	reqBody := AnthropicRequest{
		Model:     c.config.Model,
		System:    c.buildSystem(system, apiKey),
		Messages:  anthropicMsgs,
		MaxTokens: maxTokensForThinking(thinking, anthropicDefaultMaxTokens),
		Thinking:  thinking,
	}

	resp, err := c.doRequest(ctx, reqBody, apiKey)
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

	apiKey, err := c.resolveAPIKey(ctx)
	if err != nil {
		return nil, err
	}

	thinking := thinkingForLevel(c.config.ThinkLevel)
	reqBody := AnthropicRequest{
		Model:     c.config.Model,
		System:    c.buildSystem(system, apiKey),
		Messages:  anthropicMsgs,
		Tools:     anthropicTools,
		MaxTokens: maxTokensForThinking(thinking, anthropicDefaultMaxTokens),
		Thinking:  thinking,
	}

	resp, err := c.doRequest(ctx, reqBody, apiKey)
	if err != nil {
		return nil, err
	}

	result := translateResponse(resp)
	logger.Debugf("Anthropic CompleteWithTools response: tool_calls=%d", len(result.Choices[0].Message.ToolCalls))
	return result, nil
}

// CompleteStream sends messages and returns streamed response chunks.
func (c *AnthropicClient) CompleteStream(ctx context.Context, messages []Message) <-chan StreamChunk {
	chatMessages := ConvertLegacyMessages(messages)
	return c.CompleteStreamWithTools(ctx, chatMessages, nil)
}

// CompleteStreamWithTools sends messages/tools and returns streamed chunks.
func (c *AnthropicClient) CompleteStreamWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) <-chan StreamChunk {
	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)

		apiKey, err := c.resolveAPIKey(ctx)
		if err != nil {
			ch <- StreamChunk{Error: err}
			return
		}

		system, anthropicMsgs := translateMessages(messages)
		anthropicTools := translateTools(tools)
		thinking := thinkingForLevel(c.config.ThinkLevel)
		reqBody := AnthropicRequest{
			Model:     c.config.Model,
			System:    c.buildSystem(system, apiKey),
			Messages:  anthropicMsgs,
			Tools:     anthropicTools,
			Stream:    true,
			MaxTokens: maxTokensForThinking(thinking, anthropicDefaultMaxTokens),
			Thinking:  thinking,
		}

		resp, err := c.startStreamRequest(ctx, reqBody, apiKey)
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("request failed: %w", err)}
			return
		}

		if resp.StatusCode == http.StatusUnauthorized && isOAuthAccessToken(apiKey) {
			_ = resp.Body.Close()
			if refreshed, refreshErr := c.forceRefreshOAuthToken(ctx); refreshErr == nil && refreshed != "" && refreshed != apiKey {
				apiKey = refreshed
				c.config.APIKey = refreshed
				resp, err = c.startStreamRequest(ctx, reqBody, apiKey)
				if err != nil {
					ch <- StreamChunk{Error: fmt.Errorf("request failed after OAuth refresh: %w", err)}
					return
				}
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			ch <- StreamChunk{Error: fmt.Errorf("API error (status %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))}
			return
		}

		toolCalls := make(map[int]*ToolCall)
		toolCallArgs := make(map[int]*strings.Builder)
		finishReason := ""

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

		var dataLines []string
		handleEvent := func(data string) (bool, error) {
			if data == "" {
				return false, nil
			}
			if data == "[DONE]" {
				return true, nil
			}

			var evt struct {
				Type  string `json:"type"`
				Index int    `json:"index,omitempty"`
				Delta *struct {
					Type        string `json:"type,omitempty"`
					Text        string `json:"text,omitempty"`
					PartialJSON string `json:"partial_json,omitempty"`
					StopReason  string `json:"stop_reason,omitempty"`
				} `json:"delta,omitempty"`
				ContentBlock *ContentBlock `json:"content_block,omitempty"`
				Error        *struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error,omitempty"`
			}
			if err := json.Unmarshal([]byte(data), &evt); err != nil {
				// Ignore malformed keepalive chunks instead of killing stream.
				return false, nil
			}

			switch evt.Type {
			case "content_block_start":
				if evt.ContentBlock != nil && evt.ContentBlock.Type == "tool_use" {
					toolCalls[evt.Index] = &ToolCall{
						ID:   evt.ContentBlock.ID,
						Type: "function",
						Function: FunctionCall{
							Name: evt.ContentBlock.Name,
						},
					}
					if args := normalizeRawJSON(evt.ContentBlock.Input); args != "" && args != "{}" {
						toolCalls[evt.Index].Function.Arguments = args
					}
				}
			case "content_block_delta":
				if evt.Delta == nil {
					return false, nil
				}
				switch evt.Delta.Type {
				case "text_delta":
					if evt.Delta.Text != "" {
						ch <- StreamChunk{Content: evt.Delta.Text}
					}
				case "input_json_delta":
					tc, ok := toolCalls[evt.Index]
					if !ok {
						return false, nil
					}
					sb, exists := toolCallArgs[evt.Index]
					if !exists {
						sb = &strings.Builder{}
						if tc.Function.Arguments != "" {
							sb.WriteString(tc.Function.Arguments)
							tc.Function.Arguments = ""
						}
						toolCallArgs[evt.Index] = sb
					}
					sb.WriteString(evt.Delta.PartialJSON)
				}
			case "message_delta":
				if evt.Delta != nil && evt.Delta.StopReason != "" {
					finishReason = mapAnthropicStopReason(evt.Delta.StopReason)
				}
			case "error":
				if evt.Error != nil {
					return true, fmt.Errorf("Anthropic stream error: %s", evt.Error.Message)
				}
				return true, fmt.Errorf("Anthropic stream error")
			case "message_stop":
				return true, nil
			}

			return false, nil
		}

		flushData := func() (bool, error) {
			if len(dataLines) == 0 {
				return false, nil
			}
			joined := strings.Join(dataLines, "\n")
			dataLines = dataLines[:0]
			return handleEvent(joined)
		}

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				ch <- StreamChunk{Error: ctx.Err(), Done: true}
				return
			default:
			}

			line := scanner.Text()
			if line == "" {
				stop, err := flushData()
				if err != nil {
					ch <- StreamChunk{Error: err, Done: true}
					return
				}
				if stop {
					break
				}
				continue
			}

			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}

		if stop, err := flushData(); err != nil {
			ch <- StreamChunk{Error: err, Done: true}
			return
		} else if stop {
			// no-op
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("stream read error: %w", err), Done: true}
			return
		}

		if len(toolCalls) > 0 {
			indices := make([]int, 0, len(toolCalls))
			for idx := range toolCalls {
				indices = append(indices, idx)
			}
			sort.Ints(indices)

			ordered := make([]ToolCall, 0, len(indices))
			for _, idx := range indices {
				tc := toolCalls[idx]
				if tc == nil {
					continue
				}
				if sb, ok := toolCallArgs[idx]; ok {
					tc.Function.Arguments = strings.TrimSpace(sb.String())
				}
				if strings.TrimSpace(tc.Function.Arguments) == "" {
					tc.Function.Arguments = "{}"
				}
				ordered = append(ordered, *tc)
			}

			toolCallsJSON, _ := json.Marshal(ordered)
			ch <- StreamChunk{
				Content:      "\n__TOOL_CALLS__:" + string(toolCallsJSON),
				FinishReason: "tool_calls",
				Done:         true,
			}
			return
		}

		ch <- StreamChunk{FinishReason: finishReason, Done: true}
	}()

	return ch
}

func (c *AnthropicClient) doRequest(ctx context.Context, reqBody AnthropicRequest, apiKey string) (*AnthropicResponse, error) {
	statusCode, body, err := c.performRequest(ctx, reqBody, apiKey)
	if err != nil {
		return nil, err
	}

	if statusCode == http.StatusUnauthorized && isOAuthAccessToken(apiKey) {
		if refreshed, refreshErr := c.forceRefreshOAuthToken(ctx); refreshErr == nil && refreshed != "" && refreshed != apiKey {
			apiKey = refreshed
			c.config.APIKey = refreshed
			statusCode, body, err = c.performRequest(ctx, reqBody, apiKey)
			if err != nil {
				return nil, err
			}
		}
	}

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (status %d): %s", statusCode, strings.TrimSpace(string(body)))
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

func (c *AnthropicClient) performRequest(ctx context.Context, reqBody AnthropicRequest, apiKey string) (int, []byte, error) {
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	logger.Tracef("Anthropic request body (%d bytes): %.3000s", len(jsonData), string(jsonData))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.messagesURL(apiKey), bytes.NewBuffer(jsonData))
	if err != nil {
		return 0, nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.applyHeaders(req, apiKey, reqBody.Thinking, false)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to read response: %w", err)
	}
	logger.Tracef("Anthropic response body (%d bytes): %.3000s", len(body), string(body))

	return resp.StatusCode, body, nil
}

func (c *AnthropicClient) startStreamRequest(ctx context.Context, reqBody AnthropicRequest, apiKey string) (*http.Response, error) {
	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	logger.Tracef("Anthropic stream request body (%d bytes): %.3000s", len(jsonData), string(jsonData))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.messagesURL(apiKey), bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.applyHeaders(req, apiKey, reqBody.Thinking, true)

	// Streaming requests should not use the static client timeout.
	streamClient := &http.Client{}
	return streamClient.Do(req)
}

func (c *AnthropicClient) messagesURL(apiKey string) string {
	base := strings.TrimRight(c.config.BaseURL, "/") + "/v1/messages"
	if !isOAuthAccessToken(apiKey) {
		return base
	}

	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	q.Set("beta", "true")
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *AnthropicClient) applyHeaders(req *http.Request, apiKey string, thinking *ThinkingConfig, stream bool) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", anthropicVersionHeader)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}

	switch {
	case isOAuthAccessToken(apiKey):
		req.Header.Set("Authorization", "Bearer "+strings.TrimPrefix(apiKey, "oauth:"))
		req.Header.Set("anthropic-beta", anthropicOAuthBetaHeader)
		req.Header.Set("user-agent", anthropicOAuthUserAgent)
		req.Header.Set("x-app", "cli")
	case isOAuthSetupToken(apiKey):
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("anthropic-beta", anthropicSetupTokenBetaHeader)
		req.Header.Set("user-agent", anthropicOAuthUserAgent)
		req.Header.Set("x-app", "cli")
	default:
		req.Header.Set("x-api-key", apiKey)
		// Enable interleaved thinking beta for regular API keys when thinking is active.
		if thinking != nil {
			req.Header.Set("anthropic-beta", anthropicThinkingBetaHeader)
		}
	}
}

func (c *AnthropicClient) resolveAPIKey(ctx context.Context) (string, error) {
	apiKey := strings.TrimSpace(c.config.APIKey)
	if apiKey == "" {
		resolved, err := c.resolveOAuthKeyFromStore(ctx, "")
		if err != nil {
			return "", fmt.Errorf("anthropic API key is required: %w", err)
		}
		c.config.APIKey = resolved
		return resolved, nil
	}

	if isOAuthAccessToken(apiKey) {
		if resolved, err := c.resolveOAuthKeyFromStore(ctx, apiKey); err == nil && resolved != "" {
			c.config.APIKey = resolved
			return resolved, nil
		}
	}

	return apiKey, nil
}

func (c *AnthropicClient) resolveOAuthKeyFromStore(ctx context.Context, fallback string) (string, error) {
	path, err := resolveAnthropicOAuthStorePath(c.config.OAuthStorePath)
	if err != nil {
		if fallback != "" {
			return fallback, nil
		}
		return "", err
	}

	creds, err := LoadAnthropicOAuthCredentials(path)
	if err != nil {
		if fallback != "" {
			return fallback, nil
		}
		return "", err
	}

	now := time.Now()
	if creds.IsExpiringSoon(now, 2*time.Minute) {
		if strings.TrimSpace(creds.RefreshToken) == "" {
			if creds.IsExpired(now) {
				if fallback != "" {
					return fallback, nil
				}
				return "", fmt.Errorf("Anthropic OAuth token expired and refresh token is missing")
			}
		} else {
			refreshed, refreshErr := RefreshAnthropicOAuthCredentials(ctx, creds)
			if refreshErr != nil {
				if creds.IsExpired(now) {
					if fallback != "" {
						return fallback, nil
					}
					return "", fmt.Errorf("failed to refresh Anthropic OAuth token: %w", refreshErr)
				}
			} else {
				creds = refreshed
				if err := SaveAnthropicOAuthCredentials(path, creds); err != nil {
					logger.Warnf("failed to persist refreshed Anthropic OAuth token: %v", err)
				}
			}
		}
	}

	if strings.TrimSpace(creds.AccessToken) == "" {
		if fallback != "" {
			return fallback, nil
		}
		return "", fmt.Errorf("Anthropic OAuth credentials missing access token")
	}

	return "oauth:" + creds.AccessToken, nil
}

func (c *AnthropicClient) forceRefreshOAuthToken(ctx context.Context) (string, error) {
	path, err := resolveAnthropicOAuthStorePath(c.config.OAuthStorePath)
	if err != nil {
		return "", err
	}
	creds, err := LoadAnthropicOAuthCredentials(path)
	if err != nil {
		return "", err
	}
	refreshed, err := RefreshAnthropicOAuthCredentials(ctx, creds)
	if err != nil {
		return "", err
	}
	if err := SaveAnthropicOAuthCredentials(path, refreshed); err != nil {
		return "", err
	}
	return "oauth:" + refreshed.AccessToken, nil
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
			if len(msg.ContentBlocks) > 0 {
				blocks := toAnthropicUserBlocks(msg.ContentBlocks)
				if len(blocks) > 0 {
					if msg.Content != "" && !hasTextBlock(blocks) {
						blocks = append(blocks, ContentBlock{Type: "text", Text: msg.Content})
					}
					result = append(result, AnthropicMessage{Role: RoleUser, Content: blocks})
					continue
				}
			}
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
				FinishReason: mapAnthropicStopReason(resp.StopReason),
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

func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	default:
		if strings.TrimSpace(reason) == "" {
			return "stop"
		}
		return reason
	}
}

func normalizeRawJSON(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return trimmed
	}
	compact, err := json.Marshal(decoded)
	if err != nil {
		return trimmed
	}
	return string(compact)
}

func toAnthropicUserBlocks(blocks []ContentBlock) []ContentBlock {
	result := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			result = append(result, ContentBlock{Type: "text", Text: b.Text})
		case "image":
			if b.Source == nil || strings.TrimSpace(b.Source.Data) == "" {
				continue
			}
			result = append(result, ContentBlock{
				Type: "image",
				Source: &ContentSource{
					Type:      b.Source.Type,
					MediaType: b.Source.MediaType,
					Data:      b.Source.Data,
				},
			})
		}
	}
	return result
}

func hasTextBlock(blocks []ContentBlock) bool {
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return true
		}
	}
	return false
}

// isOAuthSetupToken checks if key is an OAuth setup-token from Claude Code.
func isOAuthSetupToken(key string) bool {
	return strings.Contains(strings.TrimSpace(key), "sk-ant-oat")
}

// isOAuthAccessToken checks whether key is a raw OAuth bearer token wrapper.
func isOAuthAccessToken(key string) bool {
	return strings.HasPrefix(strings.TrimSpace(key), "oauth:")
}

func isOAuthAuthKey(key string) bool {
	return isOAuthSetupToken(key) || isOAuthAccessToken(key)
}

// buildSystem wraps the system prompt with Claude Code identity for OAuth auth.
func (c *AnthropicClient) buildSystem(system string, apiKey string) interface{} {
	if !isOAuthAuthKey(apiKey) {
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
