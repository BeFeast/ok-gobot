package ai

import (
	"context"
	"fmt"
	"strings"

	"ok-gobot/internal/logger"
	"ok-gobot/internal/worker"
)

// DroidConfig holds droid-specific configuration.
type DroidConfig = worker.DroidConfig

// DroidClient implements Client and StreamingClient for factory.ai droid exec.
//
// Architecture: ok-gobot shapes message history into a task prompt, then sends
// that task through the shared worker adapter contract. The default backend is
// worker.DroidAdapter, which spawns `droid exec` as a subprocess per request.
// Droid runs the model and handles tool execution internally, including MCP
// tools registered with droid (e.g. ok-gobot's memory MCP server).
//
// Tool definitions passed via CompleteWithTools are ignored — droid discovers
// tools from its own MCP server configuration, not from per-request definitions.
//
// For multi-turn context, the full message history is formatted into a single
// prompt. Droid session management (--session-id) is not used yet; each request
// starts a fresh droid session.
type DroidClient struct {
	config   ProviderConfig
	droidCfg DroidConfig
	adapter  worker.Adapter
}

// SupportsVision reports whether droid currently accepts multimodal user blocks.
func (c *DroidClient) SupportsVision() bool {
	return false
}

// NewDroidClient creates a new droid subprocess client.
func NewDroidClient(config ProviderConfig, droidCfg DroidConfig) *DroidClient {
	return newDroidClientWithAdapter(config, droidCfg, nil)
}

func newDroidClientWithAdapter(config ProviderConfig, droidCfg DroidConfig, adapter worker.Adapter) *DroidClient {
	if droidCfg.BinaryPath == "" {
		droidCfg.BinaryPath = "droid"
	}
	if adapter == nil {
		adapter = worker.NewDroidAdapter(droidCfg)
	}
	return &DroidClient{
		config:   config,
		droidCfg: droidCfg,
		adapter:  adapter,
	}
}

func (c *DroidClient) buildRequest(prompt string) worker.Request {
	return worker.Request{
		Task:    prompt,
		Model:   c.config.Model,
		WorkDir: c.droidCfg.WorkDir,
	}
}

// formatDroidPrompt converts a legacy message history into a single prompt string.
func formatDroidPrompt(messages []Message) string {
	var sb strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			sb.WriteString("[System]\n")
			sb.WriteString(msg.Content)
			sb.WriteString("\n\n")
		case "user":
			sb.WriteString(msg.Content)
			sb.WriteString("\n")
		case "assistant":
			sb.WriteString("[Previous response]\n")
			sb.WriteString(msg.Content)
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// formatDroidChatPrompt converts ChatMessage history into a prompt string.
func formatDroidChatPrompt(messages []ChatMessage) string {
	var sb strings.Builder
	for _, msg := range messages {
		switch msg.Role {
		case RoleSystem:
			sb.WriteString("[System]\n")
			sb.WriteString(msg.Content)
			sb.WriteString("\n\n")
		case RoleUser:
			sb.WriteString(msg.Content)
			sb.WriteString("\n")
		case RoleAssistant:
			if msg.Content != "" {
				sb.WriteString("[Previous response]\n")
				sb.WriteString(msg.Content)
				sb.WriteString("\n\n")
			}
		case RoleTool:
			sb.WriteString(fmt.Sprintf("[Tool result: %s]\n", msg.Name))
			sb.WriteString(msg.Content)
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

// Complete sends messages and returns the response.
func (c *DroidClient) Complete(ctx context.Context, messages []Message) (string, error) {
	prompt := formatDroidPrompt(messages)
	logger.Debugf("Droid Complete: model=%s prompt_len=%d", c.config.Model, len(prompt))

	result, err := c.adapter.Run(ctx, c.buildRequest(prompt))
	if err != nil {
		return "", err
	}

	logger.Debugf("Droid Complete response: len=%d session=%s", len(result.Content), result.SessionID)
	return result.Content, nil
}

// CompleteWithTools sends messages with tool definitions and returns the full response.
// Tool definitions are ignored — droid discovers tools via its MCP server configuration.
func (c *DroidClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	prompt := formatDroidChatPrompt(messages)
	logger.Debugf("Droid CompleteWithTools: model=%s prompt_len=%d tools=%d (ignored, droid uses MCP)",
		c.config.Model, len(prompt), len(tools))

	result, err := c.adapter.Run(ctx, c.buildRequest(prompt))
	if err != nil {
		return nil, err
	}

	return &ChatCompletionResponse{
		ID:    result.SessionID,
		Model: c.config.Model,
		Choices: []struct {
			Index        int         `json:"index"`
			Message      ChatMessage `json:"message"`
			FinishReason string      `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    RoleAssistant,
					Content: result.Content,
				},
				FinishReason: "stop",
			},
		},
	}, nil
}

// CompleteStream sends messages and returns a channel of streamed chunks.
func (c *DroidClient) CompleteStream(ctx context.Context, messages []Message) <-chan StreamChunk {
	chatMessages := ConvertLegacyMessages(messages)
	return c.CompleteStreamWithTools(ctx, chatMessages, nil)
}

// CompleteStreamWithTools streams the droid response as chunks.
// Tool definitions are ignored — droid discovers tools via MCP.
func (c *DroidClient) CompleteStreamWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) <-chan StreamChunk {
	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)

		prompt := formatDroidChatPrompt(messages)
		logger.Debugf("Droid stream: model=%s prompt_len=%d", c.config.Model, len(prompt))
		for evt := range c.adapter.Stream(ctx, c.buildRequest(prompt)) {
			chunk := StreamChunk{
				Content: evt.Content,
				Done:    evt.Done,
				Error:   evt.Error,
			}
			if evt.Done && evt.Error == nil {
				chunk.FinishReason = "stop"
			}
			ch <- chunk
		}
	}()

	return ch
}
