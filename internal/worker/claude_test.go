package worker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewClaudeAdapter_Defaults(t *testing.T) {
	adapter := NewClaudeAdapter(ClaudeConfig{})
	if adapter.config.BinaryPath != "claude" {
		t.Fatalf("binary_path = %q, want %q", adapter.config.BinaryPath, "claude")
	}
}

func TestClaudeAdapterBuildArgs(t *testing.T) {
	tests := []struct {
		name     string
		config   ClaudeConfig
		req      Request
		outFmt   string
		wantArgs []string
	}{
		{
			name:     "minimal json",
			config:   ClaudeConfig{BinaryPath: "claude"},
			req:      Request{Task: "hello", Model: "claude-sonnet-4-5"},
			outFmt:   "json",
			wantArgs: []string{"-p", "--output-format", "json", "--model", "claude-sonnet-4-5", "hello"},
		},
		{
			name:     "stream-json no model",
			config:   ClaudeConfig{BinaryPath: "claude"},
			req:      Request{Task: "do stuff"},
			outFmt:   "stream-json",
			wantArgs: []string{"-p", "--output-format", "stream-json", "do stuff"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewClaudeAdapter(tt.config)
			got := adapter.buildArgs(tt.req, tt.outFmt)

			if len(got) != len(tt.wantArgs) {
				t.Fatalf("arg count: got %d, want %d\n  got:  %v\n  want: %v", len(got), len(tt.wantArgs), got, tt.wantArgs)
			}
			for i, g := range got {
				if g != tt.wantArgs[i] {
					t.Fatalf("arg[%d] = %q, want %q", i, g, tt.wantArgs[i])
				}
			}
		})
	}
}

func TestClaudeAdapterWorkDir(t *testing.T) {
	t.Run("request overrides adapter", func(t *testing.T) {
		adapter := NewClaudeAdapter(ClaudeConfig{WorkDir: "/tmp/default"})
		got := adapter.workDir(Request{WorkDir: "/tmp/override"})
		if got != "/tmp/override" {
			t.Fatalf("workDir = %q, want %q", got, "/tmp/override")
		}
	})

	t.Run("adapter fallback", func(t *testing.T) {
		adapter := NewClaudeAdapter(ClaudeConfig{WorkDir: "/tmp/default"})
		got := adapter.workDir(Request{})
		if got != "/tmp/default" {
			t.Fatalf("workDir = %q, want %q", got, "/tmp/default")
		}
	})

	t.Run("empty", func(t *testing.T) {
		adapter := NewClaudeAdapter(ClaudeConfig{})
		got := adapter.workDir(Request{})
		if got != "" {
			t.Fatalf("workDir = %q, want empty", got)
		}
	})
}

func TestClaudeAdapterRunBinaryNotFound(t *testing.T) {
	adapter := NewClaudeAdapter(ClaudeConfig{BinaryPath: "/nonexistent/claude-binary-12345"})

	_, err := adapter.Run(context.Background(), Request{
		Task:  "test",
		Model: "claude-sonnet-4-5",
	})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "claude exec failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClaudeAdapterRunMockJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "claude")
	mockScript := `#!/bin/sh
echo '{"result":"Hello from mock claude","is_error":false,"session_id":"sess-claude-1"}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewClaudeAdapter(ClaudeConfig{BinaryPath: mockBinary})
	result, err := adapter.Run(context.Background(), Request{
		Task:  "test",
		Model: "claude-sonnet-4-5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Hello from mock claude" {
		t.Fatalf("content = %q, want %q", result.Content, "Hello from mock claude")
	}
	if result.SessionID != "sess-claude-1" {
		t.Fatalf("session_id = %q, want %q", result.SessionID, "sess-claude-1")
	}
}

func TestClaudeAdapterRunErrorResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "claude")
	mockScript := `#!/bin/sh
echo '{"result":"rate limit","is_error":true,"session_id":""}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewClaudeAdapter(ClaudeConfig{BinaryPath: mockBinary})
	_, err := adapter.Run(context.Background(), Request{Task: "test"})
	if err == nil {
		t.Fatal("expected error for is_error=true result")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClaudeAdapterStreamMock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "claude")
	mockScript := `#!/bin/sh
echo '{"type":"assistant","subtype":"text","content":"Hello "}'
echo '{"type":"assistant","subtype":"text","content":"world!"}'
echo '{"type":"result","subtype":"success","result":"Hello world!","session_id":"sess-stream-1","is_error":false}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewClaudeAdapter(ClaudeConfig{BinaryPath: mockBinary})
	ch := adapter.Stream(context.Background(), Request{
		Task:  "test",
		Model: "claude-sonnet-4-5",
	})

	var events []Event
	for evt := range ch {
		events = append(events, evt)
	}

	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4\nevents: %+v", len(events), events)
	}
	if events[0].Content != "Hello " || events[1].Content != "world!" {
		t.Fatalf("unexpected content events: %+v", events)
	}
	if events[2].Content != "Hello world!" {
		t.Fatalf("result content = %q, want %q", events[2].Content, "Hello world!")
	}
	if !events[3].Done || events[3].Error != nil {
		t.Fatalf("final event = %+v, want done without error", events[3])
	}
}

func TestClaudeAdapterStreamError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "claude")
	mockScript := `#!/bin/sh
echo '{"type":"assistant","subtype":"text","content":"partial "}'
echo '{"type":"error","content":"service unavailable"}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewClaudeAdapter(ClaudeConfig{BinaryPath: mockBinary})
	ch := adapter.Stream(context.Background(), Request{Task: "test"})

	var gotError bool
	for evt := range ch {
		if evt.Error != nil {
			gotError = true
			if !strings.Contains(evt.Error.Error(), "service unavailable") {
				t.Fatalf("unexpected error: %v", evt.Error)
			}
		}
	}
	if !gotError {
		t.Fatal("expected error event in stream")
	}
}

func TestClaudeAdapterContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "claude")
	mockScript := `#!/bin/sh
sleep 30
echo '{"result":"should not reach","is_error":false}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewClaudeAdapter(ClaudeConfig{BinaryPath: mockBinary})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.Run(ctx, Request{Task: "test"})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
