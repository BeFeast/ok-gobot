package worker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewCodexAdapter_Defaults(t *testing.T) {
	adapter := NewCodexAdapter(CodexConfig{})
	if adapter.config.BinaryPath != "codex" {
		t.Fatalf("binary_path = %q, want %q", adapter.config.BinaryPath, "codex")
	}
}

func TestCodexAdapterBuildArgs(t *testing.T) {
	tests := []struct {
		name     string
		config   CodexConfig
		req      Request
		wantArgs []string
	}{
		{
			name:     "minimal",
			config:   CodexConfig{BinaryPath: "codex"},
			req:      Request{Task: "hello", Model: "o3"},
			wantArgs: []string{"--quiet", "--full-auto", "--model", "o3", "hello"},
		},
		{
			name:     "no model",
			config:   CodexConfig{BinaryPath: "codex"},
			req:      Request{Task: "do stuff"},
			wantArgs: []string{"--quiet", "--full-auto", "do stuff"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewCodexAdapter(tt.config)
			got := adapter.buildArgs(tt.req)

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

func TestCodexAdapterWorkDir(t *testing.T) {
	t.Run("request overrides adapter", func(t *testing.T) {
		adapter := NewCodexAdapter(CodexConfig{WorkDir: "/tmp/default"})
		got := adapter.workDir(Request{WorkDir: "/tmp/override"})
		if got != "/tmp/override" {
			t.Fatalf("workDir = %q, want %q", got, "/tmp/override")
		}
	})

	t.Run("adapter fallback", func(t *testing.T) {
		adapter := NewCodexAdapter(CodexConfig{WorkDir: "/tmp/default"})
		got := adapter.workDir(Request{})
		if got != "/tmp/default" {
			t.Fatalf("workDir = %q, want %q", got, "/tmp/default")
		}
	})

	t.Run("empty", func(t *testing.T) {
		adapter := NewCodexAdapter(CodexConfig{})
		got := adapter.workDir(Request{})
		if got != "" {
			t.Fatalf("workDir = %q, want empty", got)
		}
	})
}

func TestCodexAdapterRunBinaryNotFound(t *testing.T) {
	adapter := NewCodexAdapter(CodexConfig{BinaryPath: "/nonexistent/codex-binary-12345"})

	_, err := adapter.Run(context.Background(), Request{
		Task:  "test",
		Model: "o3",
	})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "codex exec failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCodexAdapterRunMockScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "codex")
	mockScript := `#!/bin/sh
echo "Hello from mock codex"
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewCodexAdapter(CodexConfig{BinaryPath: mockBinary})
	result, err := adapter.Run(context.Background(), Request{
		Task:  "test",
		Model: "o3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Hello from mock codex" {
		t.Fatalf("content = %q, want %q", result.Content, "Hello from mock codex")
	}
}

func TestCodexAdapterStreamMockScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "codex")
	mockScript := `#!/bin/sh
echo "Hello "
echo "world!"
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewCodexAdapter(CodexConfig{BinaryPath: mockBinary})
	ch := adapter.Stream(context.Background(), Request{
		Task:  "test",
		Model: "o3",
	})

	var events []Event
	for evt := range ch {
		events = append(events, evt)
	}

	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3\nevents: %+v", len(events), events)
	}
	if events[0].Content != "Hello " || events[1].Content != "world!" {
		t.Fatalf("unexpected content events: %+v", events)
	}
	if !events[2].Done || events[2].Error != nil {
		t.Fatalf("final event = %+v, want done without error", events[2])
	}
}

func TestCodexAdapterStreamExitError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "codex")
	mockScript := `#!/bin/sh
echo "partial output"
exit 1
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewCodexAdapter(CodexConfig{BinaryPath: mockBinary})
	ch := adapter.Stream(context.Background(), Request{Task: "test"})

	var gotError bool
	for evt := range ch {
		if evt.Error != nil {
			gotError = true
		}
	}
	if !gotError {
		t.Fatal("expected error event for non-zero exit")
	}
}

func TestCodexAdapterContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "codex")
	mockScript := `#!/bin/sh
sleep 30
echo "should not reach"
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewCodexAdapter(CodexConfig{BinaryPath: mockBinary})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.Run(ctx, Request{Task: "test"})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
