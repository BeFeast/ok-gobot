package ai

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestNewDroidClient_Defaults(t *testing.T) {
	client := NewDroidClient(ProviderConfig{Name: "droid"}, DroidConfig{})
	if client.droidCfg.BinaryPath != "droid" {
		t.Errorf("expected default binary_path 'droid', got %q", client.droidCfg.BinaryPath)
	}
}

func TestNewDroidClient_CustomBinary(t *testing.T) {
	client := NewDroidClient(ProviderConfig{Name: "droid"}, DroidConfig{BinaryPath: "/usr/local/bin/droid"})
	if client.droidCfg.BinaryPath != "/usr/local/bin/droid" {
		t.Errorf("expected custom binary_path, got %q", client.droidCfg.BinaryPath)
	}
}

func TestDroidClient_BuildArgs(t *testing.T) {
	tests := []struct {
		name      string
		config    ProviderConfig
		droidCfg  DroidConfig
		prompt    string
		outputFmt string
		wantArgs  []string
	}{
		{
			name:      "minimal",
			config:    ProviderConfig{Model: "glm-5"},
			droidCfg:  DroidConfig{BinaryPath: "droid"},
			prompt:    "hello",
			outputFmt: "json",
			wantArgs:  []string{"exec", "-m", "glm-5", "-o", "json", "hello"},
		},
		{
			name:      "with auto level",
			config:    ProviderConfig{Model: "kimi-k2.5"},
			droidCfg:  DroidConfig{BinaryPath: "droid", AutoLevel: "low"},
			prompt:    "test prompt",
			outputFmt: "stream-json",
			wantArgs:  []string{"exec", "-m", "kimi-k2.5", "-o", "stream-json", "--auto", "low", "test prompt"},
		},
		{
			name:      "with work dir",
			config:    ProviderConfig{Model: "glm-5"},
			droidCfg:  DroidConfig{BinaryPath: "droid", WorkDir: "/tmp/work"},
			prompt:    "prompt",
			outputFmt: "json",
			wantArgs:  []string{"exec", "-m", "glm-5", "-o", "json", "--cwd", "/tmp/work", "prompt"},
		},
		{
			name:      "no model",
			config:    ProviderConfig{},
			droidCfg:  DroidConfig{BinaryPath: "droid"},
			prompt:    "prompt",
			outputFmt: "json",
			wantArgs:  []string{"exec", "-o", "json", "prompt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewDroidClient(tt.config, tt.droidCfg)
			got := client.buildArgs(tt.prompt, tt.outputFmt)

			if len(got) != len(tt.wantArgs) {
				t.Fatalf("arg count: got %d, want %d\n  got:  %v\n  want: %v", len(got), len(tt.wantArgs), got, tt.wantArgs)
			}
			for i, g := range got {
				if g != tt.wantArgs[i] {
					t.Errorf("arg[%d]: got %q, want %q", i, g, tt.wantArgs[i])
				}
			}
		})
	}
}

func TestFormatDroidPrompt(t *testing.T) {
	messages := []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello!"},
		{Role: "assistant", Content: "Hi there!"},
		{Role: "user", Content: "How are you?"},
	}

	got := formatDroidPrompt(messages)

	// Check key sections are present
	if got == "" {
		t.Fatal("empty prompt")
	}
	if !contains(got, "[System]") {
		t.Error("missing [System] section")
	}
	if !contains(got, "You are a helpful assistant.") {
		t.Error("missing system content")
	}
	if !contains(got, "[Previous response]") {
		t.Error("missing [Previous response] section")
	}
	if !contains(got, "How are you?") {
		t.Error("missing latest user message")
	}
}

func TestFormatDroidChatPrompt(t *testing.T) {
	messages := []ChatMessage{
		{Role: RoleSystem, Content: "System prompt"},
		{Role: RoleUser, Content: "Question"},
		{Role: RoleAssistant, Content: "Answer"},
		{Role: RoleTool, Name: "memory_search", Content: `{"results":[]}`, ToolCallID: "tc1"},
		{Role: RoleUser, Content: "Follow-up"},
	}

	got := formatDroidChatPrompt(messages)

	if !contains(got, "[System]") {
		t.Error("missing [System] section")
	}
	if !contains(got, "[Tool result: memory_search]") {
		t.Error("missing tool result section")
	}
	if !contains(got, "Follow-up") {
		t.Error("missing follow-up message")
	}
}

func TestNewClientWithDroid_Factory(t *testing.T) {
	client, err := NewClientWithDroid(ProviderConfig{Name: "droid", Model: "glm-5"}, DroidConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := client.(*DroidClient); !ok {
		t.Error("expected *DroidClient")
	}
}

func TestNewClient_DroidDefaultModel(t *testing.T) {
	client, err := NewClient(ProviderConfig{Name: "droid"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	dc, ok := client.(*DroidClient)
	if !ok {
		t.Fatal("expected *DroidClient")
	}
	if dc.config.Model != "glm-5" {
		t.Errorf("expected default model 'glm-5', got %q", dc.config.Model)
	}
}

func TestDroidClient_Complete_BinaryNotFound(t *testing.T) {
	client := NewDroidClient(
		ProviderConfig{Name: "droid", Model: "glm-5"},
		DroidConfig{BinaryPath: "/nonexistent/droid-binary-12345"},
	)

	_, err := client.Complete(context.Background(), []Message{
		{Role: "user", Content: "test"},
	})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !contains(err.Error(), "droid exec failed") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestDroidClient_CompleteWithTools_BinaryNotFound(t *testing.T) {
	client := NewDroidClient(
		ProviderConfig{Name: "droid", Model: "glm-5"},
		DroidConfig{BinaryPath: "/nonexistent/droid-binary-12345"},
	)

	_, err := client.CompleteWithTools(context.Background(), []ChatMessage{
		{Role: RoleUser, Content: "test"},
	}, nil)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

// TestDroidClient_Complete_MockScript uses a mock shell script to test the
// complete flow without requiring the actual droid binary.
func TestDroidClient_Complete_MockScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	// Create a mock "droid" script that outputs valid JSON
	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "droid")
	mockScript := `#!/bin/sh
echo '{"type":"completion","result":"Hello from mock droid","is_error":false,"session_id":"sess-123"}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	client := NewDroidClient(
		ProviderConfig{Name: "droid", Model: "glm-5"},
		DroidConfig{BinaryPath: mockBinary},
	)

	result, err := client.Complete(context.Background(), []Message{
		{Role: "user", Content: "test"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Hello from mock droid" {
		t.Errorf("expected 'Hello from mock droid', got %q", result)
	}
}

// TestDroidClient_CompleteWithTools_MockScript tests CompleteWithTools with mock.
func TestDroidClient_CompleteWithTools_MockScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "droid")
	mockScript := `#!/bin/sh
echo '{"type":"completion","result":"Tool response","is_error":false,"session_id":"sess-456","num_turns":2}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	client := NewDroidClient(
		ProviderConfig{Name: "droid", Model: "glm-5"},
		DroidConfig{BinaryPath: mockBinary},
	)

	resp, err := client.CompleteWithTools(context.Background(), []ChatMessage{
		{Role: RoleSystem, Content: "You are helpful"},
		{Role: RoleUser, Content: "search memory"},
	}, []ToolDefinition{
		{Type: "function", Function: FunctionDefinition{Name: "memory_search"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	if resp.Choices[0].Message.Content != "Tool response" {
		t.Errorf("unexpected content: %q", resp.Choices[0].Message.Content)
	}
	if resp.Choices[0].FinishReason != "stop" {
		t.Errorf("unexpected finish_reason: %q", resp.Choices[0].FinishReason)
	}
	if resp.ID != "sess-456" {
		t.Errorf("expected session_id 'sess-456', got %q", resp.ID)
	}
}

// TestDroidClient_CompleteStream_MockScript tests streaming with mock.
func TestDroidClient_CompleteStream_MockScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "droid")
	mockScript := `#!/bin/sh
echo '{"type":"message","content":"Hello "}'
echo '{"type":"message","content":"world!"}'
echo '{"type":"completion","result":"","session_id":"sess-789"}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	client := NewDroidClient(
		ProviderConfig{Name: "droid", Model: "glm-5"},
		DroidConfig{BinaryPath: mockBinary},
	)

	ch := client.CompleteStream(context.Background(), []Message{
		{Role: "user", Content: "test"},
	})

	var chunks []StreamChunk
	for chunk := range ch {
		chunks = append(chunks, chunk)
	}

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// First two should be content chunks
	if chunks[0].Content != "Hello " {
		t.Errorf("chunk[0] content: got %q, want 'Hello '", chunks[0].Content)
	}
	if chunks[1].Content != "world!" {
		t.Errorf("chunk[1] content: got %q, want 'world!'", chunks[1].Content)
	}

	// Last chunk should be done
	lastChunk := chunks[len(chunks)-1]
	if !lastChunk.Done {
		t.Error("expected last chunk to be done")
	}
}

// TestDroidClient_Complete_ErrorResponse tests error handling.
func TestDroidClient_Complete_ErrorResponse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "droid")
	mockScript := `#!/bin/sh
echo '{"type":"error","result":"model not found","is_error":true}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	client := NewDroidClient(
		ProviderConfig{Name: "droid", Model: "nonexistent-model"},
		DroidConfig{BinaryPath: mockBinary},
	)

	_, err := client.Complete(context.Background(), []Message{
		{Role: "user", Content: "test"},
	})
	if err == nil {
		t.Fatal("expected error for error response")
	}
	if !contains(err.Error(), "model not found") {
		t.Errorf("expected 'model not found' in error, got: %v", err)
	}
}

// TestDroidClient_CompleteStream_ErrorEvent tests stream error handling.
func TestDroidClient_CompleteStream_ErrorEvent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "droid")
	mockScript := `#!/bin/sh
echo '{"type":"message","content":"partial "}'
echo '{"type":"error","content":"rate limit exceeded"}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	client := NewDroidClient(
		ProviderConfig{Name: "droid", Model: "glm-5"},
		DroidConfig{BinaryPath: mockBinary},
	)

	ch := client.CompleteStreamWithTools(context.Background(), []ChatMessage{
		{Role: RoleUser, Content: "test"},
	}, nil)

	var gotError bool
	for chunk := range ch {
		if chunk.Error != nil {
			gotError = true
			if !contains(chunk.Error.Error(), "rate limit exceeded") {
				t.Errorf("unexpected error: %v", chunk.Error)
			}
		}
	}
	if !gotError {
		t.Error("expected error chunk in stream")
	}
}

// TestDroidClient_ContextCancellation tests that context cancellation is respected.
func TestDroidClient_ContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "droid")
	// Script that sleeps — will be killed by context cancellation
	mockScript := `#!/bin/sh
sleep 30
echo '{"type":"completion","result":"should not reach"}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	client := NewDroidClient(
		ProviderConfig{Name: "droid", Model: "glm-5"},
		DroidConfig{BinaryPath: mockBinary},
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := client.Complete(ctx, []Message{
		{Role: "user", Content: "test"},
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}

func TestAvailableModels_IncludesDroid(t *testing.T) {
	models := AvailableModels()
	droidModels, ok := models["droid"]
	if !ok {
		t.Fatal("missing 'droid' in AvailableModels")
	}
	if len(droidModels) == 0 {
		t.Fatal("empty droid model list")
	}

	// Check key models are present
	found := make(map[string]bool)
	for _, m := range droidModels {
		found[m] = true
	}
	for _, expected := range []string{"glm-5", "kimi-k2.5", "minimax-m2.5"} {
		if !found[expected] {
			t.Errorf("missing droid model: %s", expected)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
