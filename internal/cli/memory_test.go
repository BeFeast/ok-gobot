package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	if _, _, _, err := runMemoryIndex(context.Background(), cfg, memStore, &stubCLIEmbedder{}, true, memoryIndexScope{Managed: true, Extra: true}); err != nil {
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
	for _, want := range []string{"Memory: ok", "Enabled: true", "Sources: 2", "Chunks: 2", "Watcher: not_running"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output: %q", want, output)
		}
	}
}

func TestMemoryStatusDisabledIsExplicit(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	cfg.Memory.Enabled = false
	cfg.SoulPath = t.TempDir()

	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	output := out.String()
	for _, want := range []string{"Memory: disabled", "Enabled: false", "Action: Set memory.enabled: true"} {
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

func TestMemoryCommandIncludesPackDebugCommand(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newMemoryCommand(cfg)
	want := map[string]bool{"pack": false, "eval": false, "search": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("memory %s command is not registered", name)
		}
	}
}

func TestMemoryEvalCommandEmitsReport(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"eval"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v\n%s", err, out.String())
	}
	output := out.String()
	for _, want := range []string{
		"Memory Retrieval Evaluation Report",
		"Status: pass",
		"Recall:",
		"Precision-ish:",
		"Privacy leaks: 0",
		"qmd_unavailable_fallback",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected %q in output: %q", want, output)
		}
	}
}

func TestMemoryEvalGateWiring(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"../../.forgejo/workflows/ci.yml", "../../.maestro/verify.sh"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		if !strings.Contains(string(data), "go run ./cmd/ok-gobot memory eval") {
			t.Fatalf("%s does not run memory retrieval eval", path)
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
	if _, _, _, err := runMemoryIndex(context.Background(), cfg, memStore, &stubCLIEmbedder{}, true, memoryIndexScope{Managed: true, Extra: true}); err != nil {
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

func TestMemorySearchReturnsRankedResultsAndJSON(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)
	cfg.Memory.Enabled = true
	cfg.SoulPath = t.TempDir()
	writeCLITestFile(t, filepath.Join(cfg.SoulPath, "MEMORY.md"), "# Memory\n\nThe user prefers Go for backend services.\n")
	writeCLITestFile(t, filepath.Join(cfg.SoulPath, "memory", "2026-04-29.md"), "# Daily\n\nWeather is sunny today.\n")

	memStore, err := memory.NewMemoryStore(store.DB())
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	if _, _, _, err := runMemoryIndex(context.Background(), cfg, memStore, &stubCLIEmbedder{}, true, memoryIndexScope{Managed: true, Extra: true}); err != nil {
		t.Fatalf("runMemoryIndex failed: %v", err)
	}

	t.Run("text output lists ranked hits", func(t *testing.T) {
		cmd := newMemoryCommand(cfg)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"search", "Go", "backend"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute error = %v\n%s", err, out.String())
		}
		output := out.String()
		for _, want := range []string{"Query: Go backend", "Results:", "#1 ", "scores:", "MEMORY.md"} {
			if !strings.Contains(output, want) {
				t.Fatalf("expected %q in output: %q", want, output)
			}
		}
	})

	t.Run("--json prints a MemoryResult array", func(t *testing.T) {
		cmd := newMemoryCommand(cfg)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs([]string{"search", "Go", "backend", "--json", "--limit", "3"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute error = %v\n%s", err, out.String())
		}
		var got []memory.MemoryResult
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatalf("Unmarshal: %v\n%s", err, out.String())
		}
		if len(got) == 0 {
			t.Fatalf("expected at least one result, got 0; output=%q", out.String())
		}
		for _, r := range got {
			if r.SourceFile == "" || r.Content == "" {
				t.Fatalf("missing fields in result: %+v", r)
			}
		}
	})
}

func TestMemorySearchRequiresMemoryEnabled(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)
	cfg.Memory.Enabled = false
	cfg.SoulPath = t.TempDir()

	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"search", "anything"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "memory.enabled is false") {
		t.Fatalf("expected memory.enabled error, got %v", err)
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
