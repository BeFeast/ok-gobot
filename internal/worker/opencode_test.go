package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNewOpenCodeAdapterDefaults(t *testing.T) {
	adapter := NewOpenCodeAdapter(OpenCodeConfig{})
	if adapter.config.BinaryPath != "opencode" {
		t.Fatalf("binary_path = %q, want %q", adapter.config.BinaryPath, "opencode")
	}
}

func TestOpenCodeAdapterBuildArgs(t *testing.T) {
	adapter := NewOpenCodeAdapter(OpenCodeConfig{})
	got := adapter.buildArgs(Request{Task: "do work", Model: "gpt-5.3-codex"})
	want := []string{"run", "--model", "gpt-5.3-codex", "do work"}
	if len(got) != len(want) {
		t.Fatalf("arg count = %d, want %d; got %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("arg[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOpenCodeAdapterStreamCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script mock not supported on Windows")
	}

	tmpDir := t.TempDir()
	mockBinary := filepath.Join(tmpDir, "opencode")
	mockScript := `#!/bin/sh
echo "first"
while true; do
  echo "tick"
  sleep 1
done
`
	if err := os.WriteFile(mockBinary, []byte(mockScript), 0o755); err != nil {
		t.Fatalf("failed to create mock: %v", err)
	}

	adapter := NewOpenCodeAdapter(OpenCodeConfig{BinaryPath: mockBinary})
	ctx, cancel := context.WithCancel(context.Background())
	ch := adapter.Stream(ctx, Request{Task: "test"})

	first := <-ch
	if first.Content != "first" {
		t.Fatalf("first event = %+v, want content first", first)
	}
	cancel()

	timeout := time.After(3 * time.Second)
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				t.Fatal("stream closed without cancellation event")
			}
			if evt.Done {
				if !errors.Is(evt.Error, context.Canceled) {
					t.Fatalf("done error = %v, want context canceled", evt.Error)
				}
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for stream cancellation")
		}
	}
}
