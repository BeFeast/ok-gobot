package agent

import (
	"context"
	"strings"
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"
)

// scriptedAIClient returns one tool call, then whatever texts the script lists,
// one per subsequent round-trip.
type scriptedAIClient struct {
	toolName    string
	toolArgs    string
	finalTexts  []string
	calls       int
	lastMessage string
}

func (c *scriptedAIClient) Complete(context.Context, []ai.Message) (string, error) {
	return "", nil
}

func (c *scriptedAIClient) SupportsVision() bool { return false }

func (c *scriptedAIClient) chatResponse(msg ai.ChatMessage, finish string) *ai.ChatCompletionResponse {
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{{Message: msg, FinishReason: finish}},
	}
}

func (c *scriptedAIClient) CompleteWithTools(_ context.Context, messages []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	c.calls++
	if len(messages) > 0 {
		c.lastMessage = messages[len(messages)-1].Content
	}
	if c.calls == 1 {
		return c.chatResponse(ai.ChatMessage{
			Role: "assistant",
			ToolCalls: []ai.ToolCall{{
				ID: "call_1", Type: "function",
				Function: ai.FunctionCall{Name: c.toolName, Arguments: c.toolArgs},
			}},
		}, "tool_calls"), nil
	}
	idx := c.calls - 2
	text := ""
	if idx < len(c.finalTexts) {
		text = c.finalTexts[idx]
	}
	return c.chatResponse(ai.ChatMessage{Role: "assistant", Content: text}, "stop"), nil
}

func emptyFinalTestAgent(t *testing.T, client ai.Client, toolOutput string) *ToolCallingAgent {
	t.Helper()
	registry := tools.NewRegistry()
	registry.Register(&mockTool{
		name:   "probe",
		desc:   "probe",
		output: toolOutput,
		schema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
	})
	return NewToolCallingAgent(client, registry, &Personality{Files: map[string]string{"IDENTITY.md": "Test Bot"}})
}

// An empty final turn on top of finished tool work used to end the run. Asking
// once more costs one round-trip and recovers the answer.
func TestEmptyFinalTurnGetsOneSecondChance(t *testing.T) {
	client := &scriptedAIClient{
		toolName:   "probe",
		toolArgs:   `{}`,
		finalTexts: []string{"", "Here is the answer."},
	}
	agent := emptyFinalTestAgent(t, client, "fetched content")

	resp, err := agent.ProcessRequest(context.Background(), "why", "")
	if err != nil {
		t.Fatalf("ProcessRequest: %v", err)
	}
	if resp.Message != "Here is the answer." {
		t.Fatalf("Message = %q, want the recovered answer", resp.Message)
	}
	if resp.IsFallback {
		t.Fatal("a recovered run must not be marked as a fallback")
	}
	if client.calls != 3 {
		t.Fatalf("calls = %d, want 3 (tool call, empty turn, retry)", client.calls)
	}
	if !strings.Contains(client.lastMessage, "produced no text") {
		t.Fatalf("retry nudge not sent; last message = %q", client.lastMessage)
	}
}

// The retry is bounded: a model that keeps returning nothing must not spin.
func TestEmptyFinalTurnRetriesOnlyOnceAndKeepsToolOutput(t *testing.T) {
	client := &scriptedAIClient{
		toolName:   "probe",
		toolArgs:   `{}`,
		finalTexts: []string{"", "", "", ""},
	}
	agent := emptyFinalTestAgent(t, client, "fetched content worth keeping")

	resp, err := agent.ProcessRequest(context.Background(), "why", "")
	if err != nil {
		t.Fatalf("ProcessRequest: %v", err)
	}
	if client.calls != 3 {
		t.Fatalf("calls = %d, want exactly 3 — the retry must not loop", client.calls)
	}
	if !resp.IsFallback {
		t.Fatal("giving up must be reported as a fallback")
	}
	if !strings.Contains(resp.Message, "fetched content worth keeping") {
		t.Fatalf("tool output was discarded:\n%s", resp.Message)
	}
}

func TestTruncateToolSummaryMarksTheCut(t *testing.T) {
	long := strings.Repeat("я", maxInlinedToolSummary+500)
	got := truncateToolSummary([]string{long}, maxInlinedToolSummary)
	if !strings.HasSuffix(got, "[…tool output truncated]") {
		t.Fatalf("truncation not marked: %q", got[len(got)-40:])
	}
	if strings.Count(got, "�") > 0 {
		t.Fatalf("truncation split a multi-byte rune")
	}
	short := truncateToolSummary([]string{"a", "b"}, maxInlinedToolSummary)
	if short != "a\n\nb" {
		t.Fatalf("short output altered: %q", short)
	}
}
