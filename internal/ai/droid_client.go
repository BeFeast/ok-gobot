package ai

import (
	"context"
	"fmt"
	"strings"

	"ok-gobot/internal/logger"
	"ok-gobot/internal/workers"
)

// DroidConfig holds droid-specific configuration.
type DroidConfig struct {
	BinaryPath string `mapstructure:"binary_path"` // Path to droid binary (default: "droid")
	AutoLevel  string `mapstructure:"auto_level"`  // Autonomy level: "", "low", "medium", "high"
	WorkDir    string `mapstructure:"work_dir"`    // Working directory for droid execution
}

// DroidClient implements Client and StreamingClient for factory.ai droid exec.
//
// Architecture: ok-gobot spawns `droid exec` as a subprocess per request.
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
	adapter  *workers.DroidAdapter
}

// SupportsVision reports whether droid currently accepts multimodal user blocks.
func (c *DroidClient) SupportsVision() bool {
	return false
}

// NewDroidClient creates a new droid subprocess client.
func NewDroidClient(config ProviderConfig, droidCfg DroidConfig) *DroidClient {
	if droidCfg.BinaryPath == "" {
		droidCfg.BinaryPath = "droid"
	}
	return &DroidClient{
		config:   config,
		droidCfg: droidCfg,
		adapter: workers.NewDroidAdapter(workers.DroidConfig{
			BinaryPath: droidCfg.BinaryPath,
			AutoLevel:  droidCfg.AutoLevel,
			WorkDir:    droidCfg.WorkDir,
		}),
	}
}

// buildArgs constructs the droid exec command arguments.
func (c *DroidClient) buildArgs(prompt string, outputFmt string) []string {
	return c.adapter.BuildArgs(prompt, outputFmt, c.config.Model, c.droidCfg.WorkDir)
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
	result, err := c.adapter.RunJSON(ctx, prompt, c.config.Model, c.droidCfg.WorkDir)
	if err != nil {
		return "", err
	}
	logger.Debugf("Droid Complete response: len=%d", len(result.Output))
	return result.Output, nil
}

// CompleteWithTools sends messages with tool definitions and returns the full response.
// Tool definitions are ignored — droid discovers tools via its MCP server configuration.
func (c *DroidClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	prompt := formatDroidChatPrompt(messages)
	logger.Debugf("Droid CompleteWithTools: model=%s prompt_len=%d tools=%d (ignored, droid uses MCP)",
		c.config.Model, len(prompt), len(tools))
	result, err := c.adapter.RunJSON(ctx, prompt, c.config.Model, c.droidCfg.WorkDir)
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
					Content: result.Output,
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
		_, err := c.adapter.Stream(ctx, prompt, c.config.Model, c.droidCfg.WorkDir, func(update workers.RunUpdate) {
			if update.Kind != "output" || update.Message == "" {
				return
			}
			ch <- StreamChunk{Content: update.Message}
		})
		if err != nil {
			ch <- StreamChunk{Error: err, Done: true}
			return
		}
		ch <- StreamChunk{Done: true, FinishReason: "stop"}
	}()

	return ch
}
