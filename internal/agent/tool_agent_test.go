package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"
)

// mockAIClient simulates an AI that returns tool calls
type mockAIClient struct {
	callCount int
	// First call returns a tool call, second call returns final text
	toolCallName string
	toolCallArgs string
	finalText    string
}

func (m *mockAIClient) Complete(ctx context.Context, messages []ai.Message) (string, error) {
	return m.finalText, nil
}

type recordingAIClient struct {
	finalText      string
	supportsVision bool
	lastMessages   []ai.ChatMessage
}

func (c *recordingAIClient) Complete(_ context.Context, _ []ai.Message) (string, error) {
	return c.finalText, nil
}

func (c *recordingAIClient) CompleteWithTools(_ context.Context, messages []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	c.lastMessages = append([]ai.ChatMessage(nil), messages...)
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Message:      ai.ChatMessage{Role: ai.RoleAssistant, Content: c.finalText},
				FinishReason: "stop",
			},
		},
	}, nil
}

func (c *recordingAIClient) SupportsVision() bool {
	return c.supportsVision
}

func (m *mockAIClient) CompleteWithTools(ctx context.Context, messages []ai.ChatMessage, toolDefs []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	m.callCount++

	if m.callCount == 1 && m.toolCallName != "" {
		// First call: return a tool call
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

	// Second call (or first if no tool call): return final text
	return &ai.ChatCompletionResponse{
		Choices: []struct {
			Index        int            `json:"index"`
			Message      ai.ChatMessage `json:"message"`
			FinishReason string         `json:"finish_reason"`
		}{
			{
				Message: ai.ChatMessage{
					Role:    "assistant",
					Content: m.finalText,
				},
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

// mockTool records calls for verification
type mockTool struct {
	name        string
	desc        string
	schema      map[string]interface{}
	executedCmd string
	executedURL string
	allArgs     []string
}

func (t *mockTool) Name() string                      { return t.name }
func (t *mockTool) Description() string               { return t.desc }
func (t *mockTool) GetSchema() map[string]interface{} { return t.schema }
func (t *mockTool) Execute(ctx context.Context, args ...string) (string, error) {
	t.allArgs = args
	if len(args) > 0 {
		t.executedCmd = args[0]
	}
	if len(args) > 1 {
		t.executedURL = args[1]
	}
	return fmt.Sprintf("OK: executed %s with %v", t.name, args), nil
}

func TestToolCallingAgent_BrowserNavigate(t *testing.T) {
	browserTool := &mockTool{
		name: "browser",
		desc: "Browser automation",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string"},
				"url":     map[string]interface{}{"type": "string"},
			},
			"required": []string{"command"},
		},
	}

	registry := tools.NewRegistry()
	registry.Register(browserTool)

	mockAI := &mockAIClient{
		toolCallName: "browser",
		toolCallArgs: `{"command":"navigate","url":"https://aliexpress.com"}`,
		finalText:    "Done! Opened aliexpress.com",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	agent := NewToolCallingAgent(mockAI, registry, personality)

	resp, err := agent.ProcessRequest(context.Background(), "open aliexpress.com", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	// Verify tool was called
	if !resp.ToolUsed {
		t.Fatal("Expected tool to be used")
	}
	if resp.ToolName != "browser" {
		t.Fatalf("Expected tool 'browser', got '%s'", resp.ToolName)
	}

	// Verify browser got correct args
	if browserTool.executedCmd != "navigate" {
		t.Fatalf("Expected command 'navigate', got '%s'", browserTool.executedCmd)
	}
	if browserTool.executedURL != "https://aliexpress.com" {
		t.Fatalf("Expected URL 'https://aliexpress.com', got '%s'", browserTool.executedURL)
	}

	t.Logf("Browser args: %v", browserTool.allArgs)
	t.Logf("Response: %s", resp.Message)
}

func TestToolCallingAgent_BrowserClickBySnapshotRef(t *testing.T) {
	browserTool := &mockTool{
		name: "browser",
		desc: "Browser automation",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command":     map[string]interface{}{"type": "string"},
				"snapshot_id": map[string]interface{}{"type": "string"},
				"ref":         map[string]interface{}{"type": "string"},
			},
			"required": []string{"command", "snapshot_id", "ref"},
		},
	}

	registry := tools.NewRegistry()
	registry.Register(browserTool)

	mockAI := &mockAIClient{
		toolCallName: "browser",
		toolCallArgs: `{"command":"click","snapshot_id":"snap-123","ref":"r7"}`,
		finalText:    "Clicked",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	agent := NewToolCallingAgent(mockAI, registry, personality)
	_, err := agent.ProcessRequest(context.Background(), "click the continue button", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	want := []string{"click", "snap-123", "r7"}
	if !reflect.DeepEqual(browserTool.allArgs, want) {
		t.Fatalf("browser args = %v, want %v", browserTool.allArgs, want)
	}
}

func TestToolCallingAgent_FileRead(t *testing.T) {
	fileTool := &mockTool{
		name: "file",
		desc: "File operations",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string"},
				"path":    map[string]interface{}{"type": "string"},
			},
			"required": []string{"command", "path"},
		},
	}

	registry := tools.NewRegistry()
	registry.Register(fileTool)

	mockAI := &mockAIClient{
		toolCallName: "file",
		toolCallArgs: `{"command":"read","path":"SOUL.md"}`,
		finalText:    "Here's the content of SOUL.md",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	agent := NewToolCallingAgent(mockAI, registry, personality)
	resp, err := agent.ProcessRequest(context.Background(), "read SOUL.md", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	if fileTool.executedCmd != "read" {
		t.Fatalf("Expected command 'read', got '%s'", fileTool.executedCmd)
	}
	if fileTool.executedURL != "SOUL.md" {
		t.Fatalf("Expected path 'SOUL.md', got '%s'", fileTool.executedURL)
	}

	t.Logf("File args: %v", fileTool.allArgs)
	t.Logf("Response: %s", resp.Message)
}

func TestToolCallingAgent_MessageTool(t *testing.T) {
	msgTool := &mockTool{
		name: "message",
		desc: "Send messages",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"to":   map[string]interface{}{"type": "string"},
				"text": map[string]interface{}{"type": "string"},
			},
			"required": []string{"to", "text"},
		},
	}

	registry := tools.NewRegistry()
	registry.Register(msgTool)

	mockAI := &mockAIClient{
		toolCallName: "message",
		toolCallArgs: `{"to":"wife","text":"I'll be late"}`,
		finalText:    "Message sent!",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	agent := NewToolCallingAgent(mockAI, registry, personality)
	resp, err := agent.ProcessRequest(context.Background(), "tell wife I'll be late", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	if msgTool.executedCmd != "wife" {
		t.Fatalf("Expected 'to' = 'wife', got '%s'", msgTool.executedCmd)
	}
	if msgTool.executedURL != "I'll be late" {
		t.Fatalf("Expected text 'I'll be late', got '%s'", msgTool.executedURL)
	}

	t.Logf("Message args: %v", msgTool.allArgs)
	t.Logf("Response: %s", resp.Message)
}

func TestToolCallingAgent_MemorySearchTool(t *testing.T) {
	memSearchTool := &mockTool{
		name: "memory_search",
		desc: "Search markdown memory chunks",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string"},
				"limit": map[string]interface{}{"type": "integer"},
			},
			"required": []string{"query"},
		},
	}

	registry := tools.NewRegistry()
	registry.Register(memSearchTool)

	mockAI := &mockAIClient{
		toolCallName: "memory_search",
		toolCallArgs: `{"query":"past decisions on memory v2","limit":3}`,
		finalText:    "Here are relevant memories.",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	agent := NewToolCallingAgent(mockAI, registry, personality)
	_, err := agent.ProcessRequest(context.Background(), "what did we decide about memory?", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	if memSearchTool.executedCmd != "past decisions on memory v2" {
		t.Fatalf("unexpected query arg: %q", memSearchTool.executedCmd)
	}
	if memSearchTool.executedURL != "3" {
		t.Fatalf("unexpected limit arg: %q", memSearchTool.executedURL)
	}
}

func TestToolCallingAgent_MemoryGetTool(t *testing.T) {
	memGetTool := &mockTool{
		name: "memory_get",
		desc: "Read markdown memory sections",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"source":      map[string]interface{}{"type": "string"},
				"header_path": map[string]interface{}{"type": "string"},
			},
			"required": []string{"source"},
		},
	}

	registry := tools.NewRegistry()
	registry.Register(memGetTool)

	mockAI := &mockAIClient{
		toolCallName: "memory_get",
		toolCallArgs: `{"source":"MEMORY.md","header_path":"Projects > OK Gobot"}`,
		finalText:    "Loaded that section.",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	agent := NewToolCallingAgent(mockAI, registry, personality)
	_, err := agent.ProcessRequest(context.Background(), "open the project section", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	if memGetTool.executedCmd != "MEMORY.md" {
		t.Fatalf("unexpected source arg: %q", memGetTool.executedCmd)
	}
	if memGetTool.executedURL != "Projects > OK Gobot" {
		t.Fatalf("unexpected header_path arg: %q", memGetTool.executedURL)
	}
}

func TestToolCallingAgent_NoToolCall(t *testing.T) {
	registry := tools.NewRegistry()

	mockAI := &mockAIClient{
		finalText: "Hello! How can I help you?",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	agent := NewToolCallingAgent(mockAI, registry, personality)
	resp, err := agent.ProcessRequest(context.Background(), "hello", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	if resp.ToolUsed {
		t.Fatal("Expected no tool to be used")
	}
	if resp.Message != "Hello! How can I help you?" {
		t.Fatalf("Unexpected message: %s", resp.Message)
	}
}

func TestToolSchemaGeneration(t *testing.T) {
	browserTool := &mockTool{
		name: "browser",
		desc: "Browser tool",
		schema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type": "string",
					"enum": []string{"navigate", "click"},
				},
				"url": map[string]interface{}{
					"type": "string",
				},
			},
			"required": []string{"command"},
		},
	}

	registry := tools.NewRegistry()
	registry.Register(browserTool)

	defs := tools.ToOpenAITools(registry.List())
	if len(defs) != 1 {
		t.Fatalf("Expected 1 tool definition, got %d", len(defs))
	}

	def := defs[0]
	if def.Function.Name != "browser" {
		t.Fatalf("Expected name 'browser', got '%s'", def.Function.Name)
	}

	// Verify schema is the custom one, not default
	var schema map[string]interface{}
	if err := json.Unmarshal(def.Function.Parameters, &schema); err != nil {
		t.Fatalf("Failed to parse schema: %v", err)
	}

	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("Expected properties in schema")
	}

	if _, hasCommand := props["command"]; !hasCommand {
		t.Fatal("Expected 'command' in schema properties")
	}
	if _, hasURL := props["url"]; !hasURL {
		t.Fatal("Expected 'url' in schema properties")
	}
	// Should NOT have default "input" field
	if _, hasInput := props["input"]; hasInput {
		t.Fatal("Should not have default 'input' field when custom schema is provided")
	}

	t.Logf("Schema: %s", string(def.Function.Parameters))
}

func TestToolCallingAgent_EventCallback(t *testing.T) {
	browserTool := &mockTool{
		name: "browser",
		desc: "Browser automation",
		schema: map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{"command": map[string]interface{}{"type": "string"}},
			"required":   []string{"command"},
		},
	}

	registry := tools.NewRegistry()
	registry.Register(browserTool)

	mockAI := &mockAIClient{
		toolCallName: "browser",
		toolCallArgs: `{"command":"navigate","url":"https://example.com"}`,
		finalText:    "Done",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	ta := NewToolCallingAgent(mockAI, registry, personality)

	var events []ToolEvent
	ta.SetToolEventCallback(func(e ToolEvent) {
		events = append(events, e)
	})

	_, err := ta.ProcessRequest(context.Background(), "navigate to example.com", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events (started + finished), got %d", len(events))
	}
	if events[0].Type != ToolEventStarted || events[0].ToolName != "browser" {
		t.Errorf("unexpected started event: %+v", events[0])
	}
	if events[1].Type != ToolEventFinished || events[1].ToolName != "browser" {
		t.Errorf("unexpected finished event: %+v", events[1])
	}
	if events[1].Err != nil {
		t.Errorf("expected no error in finished event, got %v", events[1].Err)
	}
}

func TestToolCallingAgent_ProcessRequestWithContentVisionEnabled(t *testing.T) {
	registry := tools.NewRegistry()
	client := &recordingAIClient{
		finalText:      "done",
		supportsVision: true,
	}
	agent := NewToolCallingAgent(client, registry, &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	})

	userBlocks := []ai.ContentBlock{
		{
			Type: "image",
			Source: &ai.ContentSource{
				Type:      "base64",
				MediaType: "image/jpeg",
				Data:      "aGVsbG8=",
			},
		},
		{Type: "text", Text: "describe this image"},
	}

	_, err := agent.ProcessRequestWithContent(context.Background(), "[Photo attached: ...]", userBlocks, "", nil)
	if err != nil {
		t.Fatalf("ProcessRequestWithContent failed: %v", err)
	}

	if len(client.lastMessages) == 0 {
		t.Fatal("expected recorded messages")
	}

	user := client.lastMessages[len(client.lastMessages)-1]
	if user.Role != ai.RoleUser {
		t.Fatalf("expected last message role user, got %s", user.Role)
	}
	if len(user.ContentBlocks) != 2 {
		t.Fatalf("expected multimodal content blocks to be preserved, got %d", len(user.ContentBlocks))
	}
}

func TestToolCallingAgent_ProcessRequestWithContentVisionDisabledFallsBackToText(t *testing.T) {
	registry := tools.NewRegistry()
	client := &recordingAIClient{
		finalText:      "done",
		supportsVision: false,
	}
	agent := NewToolCallingAgent(client, registry, &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	})

	_, err := agent.ProcessRequestWithContent(context.Background(), "[Photo attached: fallback]", []ai.ContentBlock{
		{Type: "text", Text: "caption"},
	}, "", nil)
	if err != nil {
		t.Fatalf("ProcessRequestWithContent failed: %v", err)
	}

	user := client.lastMessages[len(client.lastMessages)-1]
	if user.Content != "[Photo attached: fallback]" {
		t.Fatalf("expected fallback text content, got %q", user.Content)
	}
	if len(user.ContentBlocks) != 0 {
		t.Fatalf("expected no multimodal blocks for non-vision client, got %d", len(user.ContentBlocks))
	}
}

// slowTool simulates a tool that takes a configurable duration to complete.
type slowTool struct {
	name     string
	duration time.Duration
}

func (s *slowTool) Name() string                      { return s.name }
func (s *slowTool) Description() string               { return "slow tool for testing" }
func (s *slowTool) GetSchema() map[string]interface{} { return nil }
func (s *slowTool) Execute(ctx context.Context, args ...string) (string, error) {
	select {
	case <-time.After(s.duration):
		return "slow result", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestToolCallingAgent_ToolTimeoutTriggersSpawn(t *testing.T) {
	tool := &slowTool{name: "local", duration: 500 * time.Millisecond}

	registry := tools.NewRegistry()
	registry.Register(tool)

	mockAI := &mockAIClient{
		toolCallName: "local",
		toolCallArgs: `{"command":"sleep 30"}`,
		finalText:    "Done",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	agent := NewToolCallingAgent(mockAI, registry, personality)

	var spawnedTool, spawnedArgs string
	agent.SetToolTimeoutCallback(100*time.Millisecond, func(toolName, argsJSON string) string {
		spawnedTool = toolName
		spawnedArgs = argsJSON
		return "moved to subagent"
	})

	resp, err := agent.ProcessRequest(context.Background(), "run long command", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	if spawnedTool != "local" {
		t.Fatalf("expected spawn for tool 'local', got %q", spawnedTool)
	}
	if spawnedArgs != `{"command":"sleep 30"}` {
		t.Fatalf("expected spawn args, got %q", spawnedArgs)
	}

	// The model sees the timeout notification as tool result and produces its final response.
	t.Logf("Response: %s (tool spawned: %s)", resp.Message, spawnedTool)
}

func TestToolCallingAgent_FastToolNoTimeout(t *testing.T) {
	tool := &slowTool{name: "local", duration: 10 * time.Millisecond}

	registry := tools.NewRegistry()
	registry.Register(tool)

	mockAI := &mockAIClient{
		toolCallName: "local",
		toolCallArgs: `{"command":"echo hello"}`,
		finalText:    "Done",
	}

	personality := &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	}

	agent := NewToolCallingAgent(mockAI, registry, personality)

	spawned := false
	agent.SetToolTimeoutCallback(200*time.Millisecond, func(toolName, argsJSON string) string {
		spawned = true
		return "moved to subagent"
	})

	_, err := agent.ProcessRequest(context.Background(), "echo hello", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	if spawned {
		t.Fatal("fast tool should NOT trigger timeout spawn")
	}
}
