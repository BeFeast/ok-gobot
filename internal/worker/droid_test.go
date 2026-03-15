package worker

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNewDroidAdapter_Defaults(t *testing.T) {
	adapter := NewDroidAdapter(DroidConfig{})
	if adapter.config.BinaryPath != "droid" {
		t.Fatalf("binary_path = %q, want %q", adapter.config.BinaryPath, "droid")
	}
}

func TestDroidAdapterBuildArgs(t *testing.T) {
	tests := []struct {
		name      string
		config    DroidConfig
		req       Request
		outputFmt string
		wantArgs  []string
	}{
		{
			name:      "minimal",
			config:    DroidConfig{BinaryPath: "droid"},
			req:       Request{Task: "hello", Model: "glm-5"},
			outputFmt: "json",
			wantArgs:  []string{"exec", "-m", "glm-5", "-o", "json", "hello"},
		},
		{
			name:      "with auto level",
			config:    DroidConfig{BinaryPath: "droid", AutoLevel: "low"},
			req:       Request{Task: "test prompt", Model: "kimi-k2.5"},
			outputFmt: "stream-json",
			wantArgs:  []string{"exec", "-m", "kimi-k2.5", "-o", "stream-json", "--auto", "low", "test prompt"},
		},
		{
			name:      "adapter work dir fallback",
			config:    DroidConfig{BinaryPath: "droid", WorkDir: "/tmp/work"},
			req:       Request{Task: "prompt", Model: "glm-5"},
			outputFmt: "json",
			wantArgs:  []string{"exec", "-m", "glm-5", "-o", "json", "--cwd", "/tmp/work", "prompt"},
		},
		{
			name:      "request work dir overrides adapter",
			config:    DroidConfig{BinaryPath: "droid", WorkDir: "/tmp/work"},
			req:       Request{Task: "prompt", Model: "glm-5", WorkDir: "/tmp/override"},
			outputFmt: "json",
			wantArgs:  []string{"exec", "-m", "glm-5", "-o", "json", "--cwd", "/tmp/override", "prompt"},
		},
		{
			name:      "no model",
			config:    DroidConfig{BinaryPath: "droid"},
			req:       Request{Task: "prompt"},
			outputFmt: "json",
			wantArgs:  []string{"exec", "-o", "json", "prompt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter := NewDroidAdapter(tt.config)
			got := adapter.buildArgs(tt.req, tt.outputFmt)

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

func TestDroidAdapterRunBinaryNotFound(t *testing.T) {
	adapter := NewDroidAdapter(DroidConfig{BinaryPath: "/nonexistent/droid-binary-12345"})

	_, err := adapter.Run(context.Background(), Request{
		Task:  "test",
		Model: "glm-5",
	})
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
	if !strings.Contains(err.Error(), "droid exec failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDroidAdapterRunMockScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "droid")
	mockScript := `#!/bin/sh
echo '{"type":"completion","result":"Hello from mock droid","is_error":false,"session_id":"sess-123"}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewDroidAdapter(DroidConfig{BinaryPath: mockBinary})
	result, err := adapter.Run(context.Background(), Request{
		Task:  "test",
		Model: "glm-5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != "Hello from mock droid" {
		t.Fatalf("content = %q, want %q", result.Content, "Hello from mock droid")
	}
	if result.SessionID != "sess-123" {
		t.Fatalf("session_id = %q, want %q", result.SessionID, "sess-123")
	}
}

func TestDroidAdapterStreamMockScript(t *testing.T) {
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
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewDroidAdapter(DroidConfig{BinaryPath: mockBinary})
	ch := adapter.Stream(context.Background(), Request{
		Task:  "test",
		Model: "glm-5",
	})

	var events []Event
	for evt := range ch {
		events = append(events, evt)
	}

	if len(events) != 3 {
		t.Fatalf("event count = %d, want 3", len(events))
	}
	if events[0].Content != "Hello " || events[1].Content != "world!" {
		t.Fatalf("unexpected content events: %+v", events)
	}
	if !events[2].Done || events[2].Error != nil {
		t.Fatalf("final event = %+v, want done without error", events[2])
	}
}

func TestDroidAdapterStreamErrorEvent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "droid")
	mockScript := `#!/bin/sh
echo '{"type":"message","content":"partial "}'
echo '{"type":"error","content":"rate limit exceeded"}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewDroidAdapter(DroidConfig{BinaryPath: mockBinary})
	ch := adapter.Stream(context.Background(), Request{
		Task:  "test",
		Model: "glm-5",
	})

	var gotError bool
	for evt := range ch {
		if evt.Error != nil {
			gotError = true
			if !strings.Contains(evt.Error.Error(), "rate limit exceeded") {
				t.Fatalf("unexpected error: %v", evt.Error)
			}
		}
	}
	if !gotError {
		t.Fatal("expected error event in stream")
	}
}

func TestDroidAdapterContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "droid")
	mockScript := `#!/bin/sh
sleep 30
echo '{"type":"completion","result":"should not reach"}'
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewDroidAdapter(DroidConfig{BinaryPath: mockBinary})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := adapter.Run(ctx, Request{
		Task:  "test",
		Model: "glm-5",
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
}
