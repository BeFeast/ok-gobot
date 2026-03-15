package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"
)

func TestRuntimeHub_ToolTimeoutAutoSpawnNotification(t *testing.T) {
	if DefaultToolTimeout != 20*time.Second {
		t.Fatalf("DefaultToolTimeout = %s, want 20s", DefaultToolTimeout)
	}

	previousTimeout := DefaultToolTimeout
	DefaultToolTimeout = 100 * time.Millisecond
	t.Cleanup(func() {
		DefaultToolTimeout = previousTimeout
	})

	tool := &nonCancelableSlowTool{name: "local", duration: 300 * time.Millisecond}
	registry := tools.NewRegistry()
	registry.Register(tool)

	subagentStarted := make(chan string, 1)
	client := &timeoutNotificationAIClient{toolName: tool.name, subagentStarted: subagentStarted}
	resolver := &RunResolver{
		Store: &stubStore{},
		AIConfig: AIResolverConfig{
			Provider:      "test",
			Model:         "test-model",
			DefaultClient: client,
			ModelAliases:  map[string]string{},
		},
		DefaultPersonality: &Personality{
			Files: map[string]string{"IDENTITY.md": "Test Bot"},
		},
		ToolRegistry: registry,
	}
	hub := NewRuntimeHub(resolver)

	events := hub.Submit(RunRequest{
		SessionKey: "dm:999",
		ChatID:     999,
		Content:    "run long command",
		Context:    context.Background(),
	})

	var got *RunEvent
	for ev := range events {
		got = &ev
	}

	if got == nil {
		t.Fatal("expected run event")
	}
	if got.Type != RunEventDone {
		t.Fatalf("expected RunEventDone, got %s (err=%v)", got.Type, got.Err)
	}
	if got.Result == nil {
		t.Fatal("expected result")
	}
	if !strings.Contains(got.Result.Message, "moved to subagent") {
		t.Fatalf("expected user-visible auto-spawn notification, got %q", got.Result.Message)
	}

	select {
	case task := <-subagentStarted:
		if !strings.Contains(task, "Execute tool 'local' with arguments:") {
			t.Fatalf("unexpected subagent task: %q", task)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected timeout to spawn a subagent run")
	}
}

type timeoutNotificationAIClient struct {
	mu              sync.Mutex
	toolName        string
	subagentStarted chan string
}

type nonCancelableSlowTool struct {
	name     string
	duration time.Duration
}

func (t *nonCancelableSlowTool) Name() string { return t.name }
func (t *nonCancelableSlowTool) Description() string {
	return "slow tool for timeout integration testing"
}
func (t *nonCancelableSlowTool) GetSchema() map[string]interface{} { return nil }
func (t *nonCancelableSlowTool) Execute(context.Context, ...string) (string, error) {
	time.Sleep(t.duration)
	return "slow result", nil
}

func (c *timeoutNotificationAIClient) Complete(_ context.Context, _ []ai.Message) (string, error) {
	return "", nil
}

func (c *timeoutNotificationAIClient) CompleteWithTools(_ context.Context, messages []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	last := messages[len(messages)-1]

	if last.Role == ai.RoleUser {
		if strings.HasPrefix(last.Content, "Execute tool '") {
			select {
			case c.subagentStarted <- last.Content:
			default:
			}
			return timeoutAITextResponse("subagent accepted"), nil
		}
		return timeoutAIToolCallResponse(c.toolName, `{"command":"sleep 30"}`), nil
	}

	if last.Role == ai.RoleTool {
		return timeoutAITextResponse("Notification: " + last.Content), nil
	}

	return timeoutAITextResponse("done"), nil
}

func timeoutAIToolCallResponse(name, args string) *ai.ChatCompletionResponse {
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Index: 0,
				Message: ai.ChatMessage{
					Role: ai.RoleAssistant,
					ToolCalls: []ai.ToolCall{
						{
							ID:   "call_1",
							Type: "function",
							Function: ai.FunctionCall{
								Name:      name,
								Arguments: args,
							},
						},
					},
				},
				FinishReason: "tool_calls",
			},
		},
	}
}

func timeoutAITextResponse(text string) *ai.ChatCompletionResponse {
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Index:        0,
				Message:      ai.ChatMessage{Role: ai.RoleAssistant, Content: text},
				FinishReason: "stop",
			},
		},
	}
}
