package agent

import (
	"context"
	"strings"
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"
)

type incompleteStreamClient struct {
	legacyCalls            int
	completeWithToolsCalls int
	streamCalls            int
}

func (c *incompleteStreamClient) Complete(context.Context, []ai.Message) (string, error) {
	c.legacyCalls++
	return "legacy response", nil
}

func (c *incompleteStreamClient) CompleteWithTools(context.Context, []ai.ChatMessage, []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	c.completeWithToolsCalls++
	return nil, nil
}

func (c *incompleteStreamClient) CompleteStream(context.Context, []ai.Message) <-chan ai.StreamChunk {
	ch := make(chan ai.StreamChunk)
	close(ch)
	return ch
}

func (c *incompleteStreamClient) CompleteStreamWithTools(context.Context, []ai.ChatMessage, []ai.ToolDefinition) <-chan ai.StreamChunk {
	c.streamCalls++
	ch := make(chan ai.StreamChunk, 2)
	ch <- ai.StreamChunk{Content: "Partial answer "}
	ch <- ai.StreamChunk{
		Content: `
__TOOL_CALLS__:[{"id":"call_partial","type":"function","function":{"name":"trap","arguments":"{\"input\":\"never\"}"}}]`,
		Done:         true,
		FinishReason: "incomplete",
	}
	close(ch)
	return ch
}

type incompleteStreamTrapTool struct {
	calls int
}

func (t *incompleteStreamTrapTool) Name() string        { return "trap" }
func (t *incompleteStreamTrapTool) Description() string { return "must not execute" }
func (t *incompleteStreamTrapTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{"type": "string"},
		},
	}
}
func (t *incompleteStreamTrapTool) Execute(context.Context, ...string) (string, error) {
	t.calls++
	return "unexpected execution", nil
}

func TestToolCallingAgentPreservesIncompleteStreamingResponse(t *testing.T) {
	client := &incompleteStreamClient{}
	trap := &incompleteStreamTrapTool{}
	registry := tools.NewRegistry()
	registry.Register(trap)

	agent := NewToolCallingAgent(client, registry, &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	})
	var streamed strings.Builder
	resets := 0
	agent.SetDeltaCallback(func(delta string) {
		streamed.WriteString(delta)
	})
	agent.SetDeltaResetCallback(func() {
		resets++
	})

	resp, err := agent.ProcessRequest(context.Background(), "answer partially", "")
	if err != nil {
		t.Fatalf("ProcessRequest: %v", err)
	}
	if resp.Message != "Partial answer" {
		t.Fatalf("Message = %q, want preserved partial text", resp.Message)
	}
	if !resp.IsFallback {
		t.Fatal("incomplete response must be marked as fallback")
	}
	if resp.FinishReason != "incomplete" {
		t.Fatalf("FinishReason = %q, want incomplete", resp.FinishReason)
	}
	if streamed.String() != "Partial answer " {
		t.Fatalf("streamed text = %q, want partial delta", streamed.String())
	}
	if resets != 0 {
		t.Fatalf("delta resets = %d, incomplete partial text must remain visible", resets)
	}
	if trap.calls != 0 || resp.ToolUsed || resp.ToolCallsUsed != 0 {
		t.Fatalf("incomplete tool payload executed: calls=%d ToolUsed=%v ToolCallsUsed=%d", trap.calls, resp.ToolUsed, resp.ToolCallsUsed)
	}
	if client.legacyCalls != 0 {
		t.Fatalf("legacy Complete calls = %d, want zero", client.legacyCalls)
	}
	if client.completeWithToolsCalls != 0 {
		t.Fatalf("non-streaming CompleteWithTools calls = %d, want zero", client.completeWithToolsCalls)
	}
	if client.streamCalls != 1 {
		t.Fatalf("stream calls = %d, want one", client.streamCalls)
	}
}
