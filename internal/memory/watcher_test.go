package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcherDebouncesTrackedFileChanges(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewWatcher(tmpDir)
	if err != nil {
		t.Fatalf("NewWatcher failed: %v", err)
	}
	defer watcher.Stop()

	filePath := filepath.Join(tmpDir, "notes.md")
	if err := os.WriteFile(filePath, []byte("one"), 0o644); err != nil {
		t.Fatalf("write #1 failed: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if err := os.WriteFile(filePath, []byte("two"), 0o644); err != nil {
		t.Fatalf("write #2 failed: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if err := os.WriteFile(filePath, []byte("three"), 0o644); err != nil {
		t.Fatalf("write #3 failed: %v", err)
	}

	select {
	case event := <-watcher.Events():
		if event.RelativePath != "notes.md" {
			t.Fatalf("unexpected relative path: got %q", event.RelativePath)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for debounced watcher event")
	}

	select {
	case event := <-watcher.Events():
		t.Fatalf("expected one debounced event, got extra event: %+v", event)
	case <-time.After(1100 * time.Millisecond):
	}
}

func TestWatcherIgnoresNonTrackedFilesAndIgnoredDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewWatcher(tmpDir)
	if err != nil {
		t.Fatalf("NewWatcher failed: %v", err)
	}
	defer watcher.Stop()

	if err := os.WriteFile(filepath.Join(tmpDir, "data.json"), []byte(`{"a":1}`), 0o644); err != nil {
		t.Fatalf("write json failed: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "ignored.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write .git file failed: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir node_modules failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "node_modules", "ignored.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write node_modules file failed: %v", err)
	}

	select {
	case event := <-watcher.Events():
		t.Fatalf("expected no events for ignored files, got %+v", event)
	case <-time.After(1300 * time.Millisecond):
	}
}

func TestWatcherTracksNewDirectoriesRecursively(t *testing.T) {
	tmpDir := t.TempDir()

	watcher, err := NewWatcher(tmpDir)
	if err != nil {
		t.Fatalf("NewWatcher failed: %v", err)
	}
	defer watcher.Stop()

	dynamicDir := filepath.Join(tmpDir, "docs", "nested")
	if err := os.MkdirAll(dynamicDir, 0o755); err != nil {
		t.Fatalf("mkdir dynamic dir failed: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	targetFile := filepath.Join(dynamicDir, "context.yaml")
	if err := os.WriteFile(targetFile, []byte("x: 1"), 0o644); err != nil {
		t.Fatalf("write tracked file failed: %v", err)
	}

	select {
	case event := <-watcher.Events():
		if event.RelativePath != filepath.ToSlash(filepath.Join("docs", "nested", "context.yaml")) {
			t.Fatalf("unexpected relative path: got %q", event.RelativePath)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for recursive watcher event")
	}
}
