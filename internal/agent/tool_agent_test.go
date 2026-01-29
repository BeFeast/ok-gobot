package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

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

func (t *mockTool) Name() string        { return t.name }
func (t *mockTool) Description() string  { return t.desc }
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
