package bootstrap

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func writeWatcherFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func TestWatcherReloadsOnPromptFileChange(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bootstrap-watcher-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	soulPath := filepath.Join(tmpDir, "SOUL.md")
	writeWatcherFile(t, soulPath, "v1")

	reloaded := make(chan struct{}, 1)
	watcher, err := NewWatcher(tmpDir, func() {
		reloaded <- struct{}{}
	})
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer watcher.Stop()

	time.Sleep(100 * time.Millisecond)
	writeWatcherFile(t, soulPath, "v2")

	select {
	case <-reloaded:
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for reload event")
	}
}

func TestWatcherDebouncesPromptFileChanges(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bootstrap-watcher-debounce-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	soulPath := filepath.Join(tmpDir, "SOUL.md")
	writeWatcherFile(t, soulPath, "v1")

	var reloadCount atomic.Int32
	watcher, err := NewWatcher(tmpDir, func() {
		reloadCount.Add(1)
	})
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer watcher.Stop()

	time.Sleep(100 * time.Millisecond)
	writeWatcherFile(t, soulPath, "v2")
	time.Sleep(150 * time.Millisecond)
	writeWatcherFile(t, soulPath, "v3")
	time.Sleep(150 * time.Millisecond)
	writeWatcherFile(t, soulPath, "v4")

	time.Sleep(2500 * time.Millisecond)
	if got := reloadCount.Load(); got != 1 {
		t.Fatalf("expected one debounced reload, got %d", got)
	}
}

func TestWatcherIgnoresUntrackedFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "bootstrap-watcher-ignore-*")
	if err != nil {
		t.Fatalf("mktemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	writeWatcherFile(t, filepath.Join(tmpDir, "SOUL.md"), "stable")

	var reloadCount atomic.Int32
	watcher, err := NewWatcher(tmpDir, func() {
		reloadCount.Add(1)
	})
	if err != nil {
		t.Fatalf("new watcher: %v", err)
	}
	defer watcher.Stop()

	time.Sleep(100 * time.Millisecond)
	writeWatcherFile(t, filepath.Join(tmpDir, "notes.md"), "not tracked")

	time.Sleep(1500 * time.Millisecond)
	if got := reloadCount.Load(); got != 0 {
		t.Fatalf("expected no reload for untracked file, got %d", got)
	}
}
