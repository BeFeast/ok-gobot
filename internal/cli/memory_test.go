package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/memory"
)

func TestMemoryStatusShowsIndexedSources(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.SoulPath = t.TempDir()
	writeCLITestFile(t, filepath.Join(cfg.SoulPath, "MEMORY.md"), "# Memory\n\nDurable fact.")
	writeCLITestFile(t, filepath.Join(cfg.SoulPath, "memory", "2026-04-29.md"), "# Daily\n\nDaily note.")

	memStore, err := memory.NewMemoryStore(store.DB())
	if err != nil {
		t.Fatalf("memoryStoreFromDB failed: %v", err)
	}
	if _, _, err := runMemoryIndex(context.Background(), cfg, memStore, &stubCLIEmbedder{}, true); err != nil {
		t.Fatalf("runMemoryIndex failed: %v", err)
	}

	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	output := out.String()
	for _, want := range []string{"Memory enabled: true", "Sources: 2", "Chunks: 2"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output: %q", want, output)
		}
	}
}

func TestMemoryIndexRequiresMemoryEnabled(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	cfg.Memory.Enabled = false
	cfg.SoulPath = t.TempDir()

	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"index", "--force"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "memory.enabled is false") {
		t.Fatalf("expected memory.enabled error, got %v", err)
	}
}

func TestMemoryStatusDeepShowsQMDDiagnosticsWhenBinaryMissing(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.Memory.Backend = "qmd"
	cfg.Memory.EmbeddingsModel = "text-embedding-3-small"
	cfg.Memory.EmbeddingsBaseURL = "https://api.openai.com/v1"
	cfg.Memory.QMD.BinaryPath = "definitely-missing-qmd-binary"
	cfg.Memory.QMD.Index = "index"
	cfg.Memory.QMD.SearchMode = "search"
	cfg.Memory.QMD.Timeout = "1s"
	cfg.SoulPath = t.TempDir()

	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"status", "--deep"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	output := out.String()
	for _, want := range []string{"Backend: qmd", "QMD backend:", "Binary found: false", "Last error: qmd binary not found"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output: %q", want, output)
		}
	}
}

func writeCLITestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func TestMemoryStatusReportsExtraPathsAndMissingMounts(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.SoulPath = t.TempDir()
	writeCLITestFile(t, filepath.Join(cfg.SoulPath, "MEMORY.md"), "# M")

	vault := t.TempDir()
	writeCLITestFile(t, filepath.Join(vault, "notes", "today.md"), "# Today\n\nbody")

	missing := filepath.Join(t.TempDir(), "does-not-exist")
	cfg.Memory.ExtraPaths = []config.MemoryExtraPathConfig{
		{Name: "obsidian", Path: vault, Patterns: []string{"**/*.md"}},
		{Name: "homelab", Path: missing},
	}

	memStore, err := memory.NewMemoryStore(store.DB())
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	if _, _, err := runMemoryIndex(context.Background(), cfg, memStore, &stubCLIEmbedder{}, true); err != nil {
		t.Fatalf("runMemoryIndex failed: %v", err)
	}

	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	output := out.String()
	for _, want := range []string{
		"Extra paths (2)",
		"obsidian [ok]",
		"homelab [missing]",
		"path does not exist",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output: %q", want, output)
		}
	}
}

func TestMemoryStatusReportsConfigError(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.SoulPath = t.TempDir()
	cfg.Memory.ExtraPaths = []config.MemoryExtraPathConfig{
		{Name: "bad name", Path: "/tmp/x"},
	}

	if _, err := memory.NewMemoryStore(store.DB()); err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}

	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !strings.Contains(out.String(), "Extra paths: configuration error") {
		t.Fatalf("expected config error in output: %q", out.String())
	}
}

type stubCLIEmbedder struct{}

func (s *stubCLIEmbedder) GetEmbeddings(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{float32(len(texts[i])), float32(i + 1)}
	}
	return out, nil
}
