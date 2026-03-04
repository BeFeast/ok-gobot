package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"ok-gobot/internal/logger"
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
}

// NewDroidClient creates a new droid subprocess client.
func NewDroidClient(config ProviderConfig, droidCfg DroidConfig) *DroidClient {
	if droidCfg.BinaryPath == "" {
		droidCfg.BinaryPath = "droid"
	}
	return &DroidClient{
		config:   config,
		droidCfg: droidCfg,
	}
}

// droidStreamEvent represents a single event from droid's stream-json output.
type droidStreamEvent struct {
	Type    string `json:"type"`
	Content string `json:"content,omitempty"`
	Text    string `json:"text,omitempty"`
	// completion fields
	Result    string `json:"result,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	// tool event fields (informational — droid handles tools internally)
	ToolName string          `json:"tool_name,omitempty"`
	Input    json.RawMessage `json:"input,omitempty"`
}

// buildArgs constructs the droid exec command arguments.
func (c *DroidClient) buildArgs(prompt string, outputFmt string) []string {
	args := []string{"exec"}

	if c.config.Model != "" {
		args = append(args, "-m", c.config.Model)
	}

	args = append(args, "-o", outputFmt)

	if c.droidCfg.AutoLevel != "" {
		args = append(args, "--auto", c.droidCfg.AutoLevel)
	}

	if c.droidCfg.WorkDir != "" {
		args = append(args, "--cwd", c.droidCfg.WorkDir)
	}

	args = append(args, prompt)
	return args
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

	args := c.buildArgs(prompt, "json")
	cmd := exec.CommandContext(ctx, c.droidCfg.BinaryPath, args...)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("droid exec failed (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return "", fmt.Errorf("droid exec failed: %w", err)
	}

	var result struct {
		Result    string `json:"result"`
		IsError   bool   `json:"is_error"`
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		// If JSON parse fails, return raw output as text.
		return strings.TrimSpace(string(output)), nil
	}

	if result.IsError {
		return "", fmt.Errorf("droid error: %s", result.Result)
	}

	logger.Debugf("Droid Complete response: len=%d session=%s", len(result.Result), result.SessionID)
	return result.Result, nil
}

// CompleteWithTools sends messages with tool definitions and returns the full response.
// Tool definitions are ignored — droid discovers tools via its MCP server configuration.
func (c *DroidClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	prompt := formatDroidChatPrompt(messages)
	logger.Debugf("Droid CompleteWithTools: model=%s prompt_len=%d tools=%d (ignored, droid uses MCP)",
		c.config.Model, len(prompt), len(tools))

	args := c.buildArgs(prompt, "json")
	cmd := exec.CommandContext(ctx, c.droidCfg.BinaryPath, args...)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("droid exec failed (exit %d): %s", exitErr.ExitCode(), string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("droid exec failed: %w", err)
	}

	var result struct {
		Result    string `json:"result"`
		IsError   bool   `json:"is_error"`
		SessionID string `json:"session_id"`
		NumTurns  int    `json:"num_turns"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		result.Result = strings.TrimSpace(string(output))
	}

	if result.IsError {
		return nil, fmt.Errorf("droid error: %s", result.Result)
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
					Content: result.Result,
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

		args := c.buildArgs(prompt, "stream-json")
		cmd := exec.CommandContext(ctx, c.droidCfg.BinaryPath, args...)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to create stdout pipe: %w", err)}
			return
		}

		if err := cmd.Start(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("failed to start droid: %w", err)}
			return
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 2*1024*1024)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				ch <- StreamChunk{Error: ctx.Err(), Done: true}
				return
			default:
			}

			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}

			var evt droidStreamEvent
			if err := json.Unmarshal([]byte(line), &evt); err != nil {
				continue
			}

			switch evt.Type {
			case "message":
				text := evt.Content
				if text == "" {
					text = evt.Text
				}
				if text != "" {
					ch <- StreamChunk{Content: text}
				}

			case "completion":
				text := evt.Result
				if text == "" {
					text = evt.Content
				}
				if text != "" {
					ch <- StreamChunk{Content: text}
				}
				ch <- StreamChunk{Done: true, FinishReason: "stop"}
				_ = cmd.Wait()
				return

			case "error":
				errMsg := evt.Content
				if errMsg == "" {
					errMsg = evt.Result
				}
				if errMsg == "" {
					errMsg = "unknown droid error"
				}
				ch <- StreamChunk{Error: fmt.Errorf("droid: %s", errMsg), Done: true}
				_ = cmd.Wait()
				return

			case "tool_call", "tool_result":
				// Informational — droid handles tools internally.
				logger.Debugf("Droid stream tool event: type=%s tool=%s", evt.Type, evt.ToolName)
			}
		}

		if err := scanner.Err(); err != nil {
			ch <- StreamChunk{Error: fmt.Errorf("stream read error: %w", err), Done: true}
		}

		if err := cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				ch <- StreamChunk{
					Error: fmt.Errorf("droid exited with code %d: %s",
						exitErr.ExitCode(), string(exitErr.Stderr)),
					Done: true,
				}
			}
		} else {
			ch <- StreamChunk{Done: true, FinishReason: "stop"}
		}
	}()

	return ch
}
