package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/delegation"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/tools"
)

// stubStore implements SessionStore for testing.
type stubStore struct {
	modelOverride string
	activeAgent   string
	options       map[string]string
}

func (s *stubStore) GetModelOverride(chatID int64) (string, error) { return s.modelOverride, nil }
func (s *stubStore) GetActiveAgent(chatID int64) (string, error)   { return s.activeAgent, nil }
func (s *stubStore) GetSessionOption(chatID int64, key string) (string, error) {
	if s.options != nil {
		return s.options[key], nil
	}
	return "", nil
}

// stubAIClient returns a canned response for testing.
type stubAIClient struct {
	response string
}

func (c *stubAIClient) Complete(ctx context.Context, messages []ai.Message) (string, error) {
	return c.response, nil
}

func (c *stubAIClient) CompleteWithTools(ctx context.Context, messages []ai.ChatMessage, toolDefs []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Message:      ai.ChatMessage{Role: "assistant", Content: c.response},
				FinishReason: "stop",
			},
		},
		Usage: &struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{10, 5, 15},
	}, nil
}

func newTestResolver(response string) *RunResolver {
	return &RunResolver{
		Store: &stubStore{},
		AIConfig: AIResolverConfig{
			Provider:      "test",
			Model:         "test-model",
			DefaultClient: &stubAIClient{response: response},
			ModelAliases:  map[string]string{},
		},
		DefaultPersonality: &Personality{
			Files: map[string]string{"IDENTITY.md": "Test Bot"},
		},
		ToolRegistry: tools.NewRegistry(),
	}
}

func TestRuntimeHub_SubmitAndReceiveResult(t *testing.T) {
	resolver := newTestResolver("Hello from the hub!")
	hub := NewRuntimeHub(resolver)

	events := hub.Submit(RunRequest{
		SessionKey: "dm:123",
		ChatID:     123,
		Content:    "hi",
		Context:    context.Background(),
	})

	var got *RunEvent
	for ev := range events {
		got = &ev
	}

	if got == nil {
		t.Fatal("expected a run event, got nil")
	}
	if got.Type != RunEventDone {
		t.Fatalf("expected RunEventDone, got %s (err=%v)", got.Type, got.Err)
	}
	if got.Result.Message != "Hello from the hub!" {
		t.Fatalf("unexpected message: %s", got.Result.Message)
	}
	if got.ProfileName != "default" {
		t.Fatalf("expected profile 'default', got '%s'", got.ProfileName)
	}
}

func TestRuntimeHub_SubmitAndWaitRejectsFallbackResult(t *testing.T) {
	hub := NewRuntimeHub(newTestResolver(""))

	result, err := hub.SubmitAndWait(context.Background(), 123, "run browser task", delegation.Job{})
	if err == nil || !strings.Contains(err.Error(), "subagent returned fallback result") {
		t.Fatalf("SubmitAndWait result=%q error=%v", result, err)
	}
}

func TestRuntimeHub_SubmitAndWaitReturnsExtractedWorkOnBudgetStop(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&mockTool{
		name:   "browser",
		desc:   "Browser automation",
		output: "Lenovo Yoga Slim 7 13, ₪4290, https://www.zap.co.il/listing/123",
		schema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
		},
	})
	resolver := &RunResolver{
		Store: &stubStore{},
		AIConfig: AIResolverConfig{
			Provider:      "test",
			Model:         "test-model",
			DefaultClient: &endlessToolCallClient{},
			ModelAliases:  map[string]string{},
		},
		DefaultPersonality: &Personality{
			Files: map[string]string{"IDENTITY.md": "Test Bot"},
		},
		ToolRegistry: reg,
	}
	hub := NewRuntimeHub(resolver)

	result, err := hub.SubmitAndWait(context.Background(), 123, "find a 13 inch laptop", delegation.Job{MaxToolCalls: 2})
	if err != nil {
		t.Fatalf("SubmitAndWait error=%v, want the extracted listings", err)
	}
	if !strings.Contains(result, "Lenovo Yoga Slim 7") {
		t.Fatalf("extracted listings were discarded:\n%s", result)
	}
}

func TestRuntimeHub_PreflightFailureBlocksBeforeRunStarts(t *testing.T) {
	resolver := newTestResolver("should not run")
	resolver.AIConfig.BackendPreflight = func(context.Context, string, string, string) (ai.BackendHealth, error) {
		return ai.BackendHealth{
			Identity: ai.BackendIdentity{Provider: "anthropic", Backend: "anthropic", Model: "test-model"},
			Status:   ai.BackendHealthAuthFailed,
			Fallback: ai.FallbackDecision{Action: ai.FallbackActionStop, Reason: "auth requires operator action"},
		}, errors.New("authentication failed")
	}
	hub := NewRuntimeHub(resolver)
	started := false

	events := hub.Submit(RunRequest{
		SessionKey: "dm:preflight",
		ChatID:     123,
		Content:    "hi",
		Context:    context.Background(),
		OnRunStarted: func(RunStartInfo) {
			started = true
		},
	})

	var got *RunEvent
	for ev := range events {
		got = &ev
	}
	if got == nil || got.Type != RunEventError {
		t.Fatalf("expected preflight error event, got %+v", got)
	}
	if !strings.Contains(got.Err.Error(), "backend preflight blocked session start") {
		t.Fatalf("unexpected error: %v", got.Err)
	}
	if started {
		t.Fatal("OnRunStarted must not fire when backend preflight blocks the run")
	}
	hub.mu.Lock()
	active := len(hub.active)
	hub.mu.Unlock()
	if active != 0 {
		t.Fatalf("active runs=%d, want 0", active)
	}
}

func TestRuntimeHub_OnRunStartedUsesResolvedBackendMetadata(t *testing.T) {
	resolver := newTestResolver("resolved")
	resolver.AIConfig.DefaultThinking = "high"
	resolver.AIConfig.BackendPreflight = func(ctx context.Context, model, tier, effort string) (ai.BackendHealth, error) {
		if model != "test-model" {
			t.Fatalf("preflight model=%q, want test-model", model)
		}
		if tier != "agent" {
			t.Fatalf("preflight tier=%q, want agent", tier)
		}
		if effort != "high" {
			t.Fatalf("preflight effort=%q, want high", effort)
		}
		return ai.BackendHealth{
			Identity: ai.BackendIdentity{
				Provider: "anthropic",
				Backend:  "opencode",
				Model:    "fallback-model",
				Tier:     tier,
				Effort:   effort,
			},
			Status: ai.BackendHealthHealthy,
			Fallback: ai.FallbackDecision{
				Action:    ai.FallbackActionFallback,
				FromModel: model,
				ToModel:   "fallback-model",
				Reason:    "rate_limit is fallbackable",
			},
		}, nil
	}
	hub := NewRuntimeHub(resolver)

	var started []RunStartInfo
	events := hub.Submit(RunRequest{
		SessionKey: "dm:resolved",
		ChatID:     321,
		Content:    "hi",
		Context:    context.Background(),
		OnRunStarted: func(info RunStartInfo) {
			started = append(started, info)
		},
	})

	var got *RunEvent
	for ev := range events {
		got = &ev
	}
	if got == nil || got.Type != RunEventDone {
		t.Fatalf("expected successful run, got %+v", got)
	}
	if len(started) != 1 {
		t.Fatalf("OnRunStarted calls=%d, want 1", len(started))
	}
	info := started[0]
	if info.Model != "fallback-model" {
		t.Fatalf("started model=%q, want fallback-model", info.Model)
	}
	if info.ModelTier != "agent" {
		t.Fatalf("started tier=%q, want agent", info.ModelTier)
	}
	if info.Effort != "high" {
		t.Fatalf("started effort=%q, want high", info.Effort)
	}
	if info.Backend != "opencode" {
		t.Fatalf("started backend=%q, want opencode", info.Backend)
	}
	if info.BackendHealth.Status != ai.BackendHealthHealthy {
		t.Fatalf("started health=%q, want healthy", info.BackendHealth.Status)
	}
}

func TestRuntimeHub_CancelRun(t *testing.T) {
	// Use a slow AI client to give us time to cancel.
	slowClient := &slowAIClient{delay: 2 * time.Second}
	resolver := &RunResolver{
		Store: &stubStore{},
		AIConfig: AIResolverConfig{
			Provider:      "test",
			Model:         "test-model",
			DefaultClient: slowClient,
			ModelAliases:  map[string]string{},
		},
		DefaultPersonality: &Personality{
			Files: map[string]string{"IDENTITY.md": "Test Bot"},
		},
		ToolRegistry: tools.NewRegistry(),
	}
	hub := NewRuntimeHub(resolver)

	events := hub.Submit(RunRequest{
		SessionKey: "dm:456",
		ChatID:     456,
		Content:    "slow request",
		Context:    context.Background(),
	})

	// Give the goroutine a moment to start.
	time.Sleep(50 * time.Millisecond)

	if !hub.IsActive("dm:456") {
		t.Fatal("expected run to be active")
	}

	hub.Cancel("dm:456")

	var got *RunEvent
	for ev := range events {
		got = &ev
	}

	if got == nil {
		t.Fatal("expected a run event after cancel")
	}
	if got.Type != RunEventError {
		t.Fatalf("expected RunEventError after cancel, got %s", got.Type)
	}

	if hub.IsActive("dm:456") {
		t.Fatal("expected run to be inactive after cancel")
	}
}

func TestRuntimeHub_SubmitCancelsExisting(t *testing.T) {
	slowClient := &slowAIClient{delay: 5 * time.Second}
	resolver := &RunResolver{
		Store: &stubStore{},
		AIConfig: AIResolverConfig{
			Provider:      "test",
			Model:         "test-model",
			DefaultClient: slowClient,
			ModelAliases:  map[string]string{},
		},
		DefaultPersonality: &Personality{
			Files: map[string]string{"IDENTITY.md": "Test Bot"},
		},
		ToolRegistry: tools.NewRegistry(),
	}
	hub := NewRuntimeHub(resolver)

	// First submit — will be slow.
	events1 := hub.Submit(RunRequest{
		SessionKey: "dm:789",
		ChatID:     789,
		Content:    "first",
		Context:    context.Background(),
	})
	time.Sleep(50 * time.Millisecond)

	// Second submit for the same session — should cancel the first.
	fastClient := &stubAIClient{response: "second done"}
	resolver.AIConfig.DefaultClient = fastClient

	events2 := hub.Submit(RunRequest{
		SessionKey: "dm:789",
		ChatID:     789,
		Content:    "second",
		Context:    context.Background(),
	})

	// First run should have been cancelled.
	var ev1 *RunEvent
	for ev := range events1 {
		ev1 = &ev
	}
	if ev1 == nil || ev1.Type != RunEventError {
		t.Fatalf("expected first run to error (cancelled), got %v", ev1)
	}

	// Second run should succeed.
	var ev2 *RunEvent
	for ev := range events2 {
		ev2 = &ev
	}
	if ev2 == nil || ev2.Type != RunEventDone {
		t.Fatalf("expected second run to succeed, got %v", ev2)
	}
}

func TestRuntimeHub_ToolEventCallback(t *testing.T) {
	// Use a tool-calling AI mock.
	toolMock := &mockToolForHub{name: "test_tool", desc: "A test tool"}
	reg := tools.NewRegistry()
	reg.Register(toolMock)

	aiMock := &toolCallingAIMock{
		toolCallName: "test_tool",
		toolCallArgs: `{"input":"hello"}`,
		finalText:    "Done!",
	}

	resolver := &RunResolver{
		Store: &stubStore{},
		AIConfig: AIResolverConfig{
			Provider:      "test",
			Model:         "test-model",
			DefaultClient: aiMock,
			ModelAliases:  map[string]string{},
		},
		DefaultPersonality: &Personality{
			Files: map[string]string{"IDENTITY.md": "Test Bot"},
		},
		ToolRegistry: reg,
	}
	hub := NewRuntimeHub(resolver)

	var mu sync.Mutex
	var toolEvents []ToolEvent

	events := hub.Submit(RunRequest{
		SessionKey: "dm:100",
		ChatID:     100,
		Content:    "use the tool",
		Context:    context.Background(),
		OnToolEvent: func(ev ToolEvent) {
			mu.Lock()
			toolEvents = append(toolEvents, ev)
			mu.Unlock()
		},
	})

	for range events {
	}

	mu.Lock()
	defer mu.Unlock()

	if len(toolEvents) != 2 {
		t.Fatalf("expected 2 tool events (started+finished), got %d", len(toolEvents))
	}
	if toolEvents[0].Type != ToolEventStarted {
		t.Errorf("expected started event, got %s", toolEvents[0].Type)
	}
	if toolEvents[1].Type != ToolEventFinished {
		t.Errorf("expected finished event, got %s", toolEvents[1].Type)
	}
}

func TestTimeoutSubagentMemoryScopeInheritsParentScope(t *testing.T) {
	req := RunRequest{
		SessionKey: "dm:42",
		ChatID:     42,
		MemoryScope: memory.RecallContext{
			UserID:             777,
			ChatID:             42,
			SessionKey:         "dm:42",
			ChatType:           "private",
			AllowGlobalPrivate: true,
			ExtraPathLabels:    []string{"obsidian"},
		},
	}

	got := timeoutSubagentMemoryScope(req)
	if got.UserID != 777 || got.ChatID != 42 || got.SessionKey != "dm:42" {
		t.Fatalf("unexpected inherited memory scope: %+v", got)
	}
	if !got.AllowGlobalPrivate {
		t.Fatalf("expected inherited private-global allowance: %+v", got)
	}
	if len(got.ExtraPathLabels) != 1 || got.ExtraPathLabels[0] != "obsidian" {
		t.Fatalf("expected inherited extra path labels, got %+v", got.ExtraPathLabels)
	}
}

func TestTimeoutSubagentMemoryScopeDefaultsFromParentRequest(t *testing.T) {
	got := timeoutSubagentMemoryScope(RunRequest{
		SessionKey: "dm:99",
		ChatID:     99,
	})
	if got.ChatID != 99 {
		t.Fatalf("expected parent chat id, got %+v", got)
	}
	if got.SessionKey != "dm:99" {
		t.Fatalf("expected parent session key, got %+v", got)
	}
}

func TestRuntimeHub_Overrides(t *testing.T) {
	store := &stubStore{
		modelOverride: "session-model",
		options:       map[string]string{"think_level": "low"},
	}
	resolver := &RunResolver{
		Store: store,
		AIConfig: AIResolverConfig{
			Provider:      "test",
			Model:         "default-model",
			DefaultClient: &stubAIClient{response: "ok"},
			ModelAliases:  map[string]string{},
		},
		DefaultPersonality: &Personality{
			Files: map[string]string{"IDENTITY.md": "Test Bot"},
		},
		ToolRegistry: tools.NewRegistry(),
	}

	// Explicit overrides should take priority over session store values.
	components, err := resolver.Resolve(42, &RunOverrides{
		Model:      "explicit-model",
		ThinkLevel: "high",
	}, nil)
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	if components.Agent.ThinkLevel != "high" {
		t.Errorf("expected think level 'high', got '%s'", components.Agent.ThinkLevel)
	}
}

// TestRuntimeHubIsActiveAndCancel verifies IsActive and Cancel semantics.
func TestRuntimeHubIsActiveAndCancel(t *testing.T) {
	hub := NewRuntimeHub(newTestResolver("ok"))
	key := NewDMSessionKey(100)

	// Before any run, IsActive must be false.
	if hub.IsActive(key) {
		t.Fatal("expected IsActive=false before any submit")
	}

	// Cancel on an idle session must not panic.
	hub.Cancel(key)

	// Inject a slot directly to simulate an active run without needing a real agent.
	ctx, cancel := context.WithCancel(context.Background())
	hub.mu.Lock()
	hub.active[key] = &runSlot{cancel: cancel}
	hub.mu.Unlock()

	if !hub.IsActive(key) {
		t.Fatal("expected IsActive=true after slot injection")
	}

	hub.Cancel(key)

	select {
	case <-ctx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Cancel did not cancel the slot context within 100ms")
	}

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("expected context.Canceled after Cancel, got %v", ctx.Err())
	}

	hub.mu.Lock()
	delete(hub.active, key)
	hub.mu.Unlock()

	if hub.IsActive(key) {
		t.Fatal("expected IsActive=false after slot removal")
	}
}

// TestRuntimeHubSessionKeyFormats verifies the canonical session key helpers.
func TestRuntimeHubSessionKeyFormats(t *testing.T) {
	dm := NewDMSessionKey(12345)
	group := NewGroupSessionKey(67890)

	if dm != "dm:12345" {
		t.Errorf("DM key = %q, want %q", dm, "dm:12345")
	}
	if group != "group:67890" {
		t.Errorf("Group key = %q, want %q", group, "group:67890")
	}
}

// slowAIClient blocks for delay before returning an error (simulates long-running request).
type slowAIClient struct {
	delay time.Duration
}

func (c *slowAIClient) Complete(ctx context.Context, messages []ai.Message) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(c.delay):
		return "done", nil
	}
}

func (c *slowAIClient) CompleteWithTools(ctx context.Context, messages []ai.ChatMessage, toolDefs []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(c.delay):
		return &ai.ChatCompletionResponse{
			Choices: []struct {
				Index        int            `json:"index"`
				Message      ai.ChatMessage `json:"message"`
				FinishReason string         `json:"finish_reason"`
			}{
				{
					Message:      ai.ChatMessage{Role: "assistant", Content: "done"},
					FinishReason: "stop",
				},
			},
		}, nil
	}
}

// mockToolForHub is a simple tool that records execution.
type mockToolForHub struct {
	name string
	desc string
}

func (t *mockToolForHub) Name() string                      { return t.name }
func (t *mockToolForHub) Description() string               { return t.desc }
func (t *mockToolForHub) GetSchema() map[string]interface{} { return nil }
func (t *mockToolForHub) Execute(ctx context.Context, args ...string) (string, error) {
	return fmt.Sprintf("OK: %s", t.name), nil
}

// toolCallingAIMock returns a tool call on the first invocation, then a final response.
type toolCallingAIMock struct {
	callCount    int
	toolCallName string
	toolCallArgs string
	finalText    string
}

func (m *toolCallingAIMock) Complete(ctx context.Context, messages []ai.Message) (string, error) {
	return m.finalText, nil
}

func (m *toolCallingAIMock) CompleteWithTools(ctx context.Context, messages []ai.ChatMessage, toolDefs []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	m.callCount++
	if m.callCount == 1 && m.toolCallName != "" {
		return &ai.ChatCompletionResponse{
			Choices: []struct {
				Index        int            `json:"index"`
				Message      ai.ChatMessage `json:"message"`
				FinishReason string         `json:"finish_reason"`
			}{
				{
					Message: ai.ChatMessage{
						Role: "assistant",
						ToolCalls: []ai.ToolCall{
							{
								ID:   "call_1",
								Type: "function",
								Function: ai.FunctionCall{
									Name:      m.toolCallName,
									Arguments: m.toolCallArgs,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
			Usage: &struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{10, 5, 15},
		}, nil
	}
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Message:      ai.ChatMessage{Role: "assistant", Content: m.finalText},
				FinishReason: "stop",
			},
		},
		Usage: &struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		}{10, 5, 15},
	}, nil
}
