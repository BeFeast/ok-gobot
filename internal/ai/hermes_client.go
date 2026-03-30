package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ok-gobot/internal/logger"
)

// HermesClient wraps an OpenAI-compatible HTTP client to support the native
// Hermes tool call format used by NousResearch Hermes models.
//
// When running Hermes models locally (e.g. hermes3 via Ollama, or any model
// via vLLM with --tool-call-parser hermes), the model embeds tool calls as
// <tool_call>...</tool_call> blocks in the response content rather than
// using the OpenAI tool_calls field.
//
// HermesClient post-processes every CompleteWithTools response:
//  1. If tool_calls are already present in the response (vLLM native path),
//     no extra work is done.
//  2. Otherwise, the response content is scanned for <tool_call> blocks via
//     ParseHermesToolCalls and extracted into the ToolCalls field.
//
// For streaming, the entire content is buffered, scanned at the end, and the
// standard __TOOL_CALLS__: marker is injected so the existing tool-agent loop
// can process tool calls without changes.
type HermesClient struct {
	inner  *OpenAICompatibleClient
	config ProviderConfig
}

// SupportsVision delegates to the inner client.
func (c *HermesClient) SupportsVision() bool {
	return c.inner.SupportsVision()
}

// newHermesClient creates a HermesClient backed by an OpenAICompatibleClient.
func newHermesClient(config ProviderConfig) *HermesClient {
	inner := &OpenAICompatibleClient{
		config:     config,
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
	return &HermesClient{inner: inner, config: config}
}

// Complete delegates to the inner client (no tool calls involved).
func (c *HermesClient) Complete(ctx context.Context, messages []Message) (string, error) {
	return c.inner.Complete(ctx, messages)
}

// CompleteWithTools calls the inner OpenAI-compatible endpoint and
// post-processes the response to extract Hermes-format tool calls from
// the content when the model did not use the structured tool_calls field.
func (c *HermesClient) CompleteWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) (*ChatCompletionResponse, error) {
	resp, err := c.inner.CompleteWithTools(ctx, messages, tools)
	if err != nil {
		return nil, err
	}

	if len(resp.Choices) == 0 {
		return resp, nil
	}

	choice := &resp.Choices[0]

	// If the model already returned structured tool calls, nothing to do.
	if len(choice.Message.ToolCalls) > 0 {
		return resp, nil
	}

	// Attempt to extract Hermes-format tool calls from the content.
	cleanContent, toolCalls := ParseHermesToolCalls(choice.Message.Content)
	if len(toolCalls) == 0 {
		return resp, nil
	}

	logger.Debugf("HermesClient: extracted %d tool call(s) from content", len(toolCalls))

	choice.Message.Content = cleanContent
	choice.Message.ToolCalls = toolCalls
	choice.FinishReason = "tool_calls"

	return resp, nil
}

// CompleteStream delegates to the inner client (no Hermes-specific handling needed).
func (c *HermesClient) CompleteStream(ctx context.Context, messages []Message) <-chan StreamChunk {
	return c.inner.CompleteStream(ctx, messages)
}

// CompleteStreamWithTools streams the response, buffers it, and injects the
// standard __TOOL_CALLS__: marker when Hermes-format tool calls are detected.
// This preserves compatibility with the existing tool-agent streaming loop.
func (c *HermesClient) CompleteStreamWithTools(ctx context.Context, messages []ChatMessage, tools []ToolDefinition) <-chan StreamChunk {
	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)

		inner := c.inner.CompleteStreamWithTools(ctx, messages, tools)

		const toolCallMarker = "\n__TOOL_CALLS__:"
		var contentBuf strings.Builder
		var alreadyHasToolCalls bool

		for chunk := range inner {
			if chunk.Error != nil {
				ch <- chunk
				return
			}

			content := chunk.Content

			// If the inner stream already emitted the tool-calls marker
			// (structured tool_calls via SSE), forward everything as-is.
			if strings.Contains(content, toolCallMarker) {
				alreadyHasToolCalls = true
			}

			if alreadyHasToolCalls {
				ch <- chunk
				if chunk.Done {
					return
				}
				continue
			}

			// Buffer content to scan for Hermes tags at the end.
			if content != "" {
				contentBuf.WriteString(content)
				// Forward content delta to caller so streaming text appears live.
				ch <- StreamChunk{Content: content}
			}

			if chunk.Done {
				break
			}
		}

		if alreadyHasToolCalls {
			return
		}

		// Scan the buffered content for <tool_call> blocks.
		fullContent := contentBuf.String()
		cleanContent, toolCalls := ParseHermesToolCalls(fullContent)

		if len(toolCalls) == 0 {
			ch <- StreamChunk{Done: true, FinishReason: "stop"}
			return
		}

		logger.Debugf("HermesClient stream: extracted %d tool call(s) from content", len(toolCalls))

		// The streaming text already forwarded the raw content (with <tool_call>
		// blocks). Signal the tool-agent to discard accumulated text and use
		// the extracted tool calls instead.
		_ = cleanContent
		toolCallsJSON, _ := json.Marshal(toolCalls)
		ch <- StreamChunk{
			Content: "\n__TOOL_CALLS__:" + string(toolCallsJSON),
			Done:    true,
		}
	}()

	return ch
}
