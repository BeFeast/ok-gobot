package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/memory"
)

func TestLoadFromConfigWithOptions_RegistersMemoryV2Tools(t *testing.T) {
	tmpDir := t.TempDir()

	registry, err := LoadFromConfigWithOptions(tmpDir, &ToolsConfig{
		MemoryManager: memory.NewMemoryManager(nil, nil),
	})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions failed: %v", err)
	}

	if _, ok := registry.Get("memory_search"); !ok {
		t.Fatal("memory_search tool is not registered")
	}
	if _, ok := registry.Get("memory_get"); !ok {
		t.Fatal("memory_get tool is not registered")
	}
	if _, ok := registry.Get("memory"); ok {
		t.Fatal("legacy memory tool should not be registered")
	}
}

func TestMemoryGetTool_ReadSectionByHeaderPath(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "MEMORY.md")

	markdown := strings.Join([]string{
		"# Memory",
		"",
		"## Projects",
		"High-level project notes.",
		"",
		"### OK Gobot",
		"- migrate memory to v2",
		"- remove legacy save/list/forget",
		"",
		"## Personal",
		"- run 5k",
	}, "\n")

	if err := os.WriteFile(path, []byte(markdown), 0644); err != nil {
		t.Fatalf("failed to write markdown fixture: %v", err)
	}

	tool := NewMemoryGetTool(tmpDir)
	got, err := tool.Execute(context.Background(), "MEMORY.md", "Projects > OK Gobot")
	if err != nil {
		t.Fatalf("memory_get failed: %v", err)
	}

	if !strings.Contains(got, "### OK Gobot") {
		t.Fatalf("expected section heading in result, got:\n%s", got)
	}
	if strings.Contains(got, "## Personal") {
		t.Fatalf("expected section-scoped output, got:\n%s", got)
	}
}

func TestMemoryGetTool_PathTraversalBlocked(t *testing.T) {
	tmpDir := t.TempDir()

	tool := NewMemoryGetTool(tmpDir)
	if _, err := tool.Execute(context.Background(), "../outside.md"); err == nil {
		t.Fatal("expected path traversal to fail")
	}
}

func TestMemoryGetTool_ReadsExtraPathSource(t *testing.T) {
	soul := t.TempDir()
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "notes"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "# Note\n\nVault content."
	if err := os.WriteFile(filepath.Join(vault, "notes", "today.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewMemoryGetTool(soul).WithExtraPaths([]memory.ExtraPath{{
		Name:     "obsidian",
		Path:     vault,
		Patterns: []string{"**/*.md"},
		ReadOnly: true,
	}})

	got, err := tool.Execute(context.Background(), "extra:obsidian/notes/today.md")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(got, "Vault content.") {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestMemoryGetTool_RejectsTraversalInExtraSource(t *testing.T) {
	soul := t.TempDir()
	vault := t.TempDir()

	tool := NewMemoryGetTool(soul).WithExtraPaths([]memory.ExtraPath{{
		Name: "obsidian", Path: vault, Patterns: []string{"**/*.md"}, ReadOnly: true,
	}})

	if _, err := tool.Execute(context.Background(), "extra:obsidian/../escape.md"); err == nil {
		t.Fatal("expected traversal rejection")
	}
}

func TestMemoryGetTool_UnknownExtraCollectionFails(t *testing.T) {
	soul := t.TempDir()

	tool := NewMemoryGetTool(soul) // no extras configured
	if _, err := tool.Execute(context.Background(), "extra:obsidian/notes/x.md"); err == nil {
		t.Fatal("expected unknown collection error")
	}
}

func TestMemoryGetTool_ScopedPolicyDeniesOtherUserSource(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "telegram", "users", "2002", "memory", "2026-04-30.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create fixture dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("other user secret"), 0644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	policy := memory.NewRecallPolicy(memory.RecallContext{UserID: 1001, ChatID: 1001, ChatType: "private", SessionKey: "dm:1001"})
	tool := NewScopedMemoryGetTool(tmpDir, policy)
	_, err := tool.Execute(context.Background(), "telegram/users/2002/memory/2026-04-30.md")
	if err == nil || !strings.Contains(err.Error(), "denied by recall policy") {
		t.Fatalf("expected recall policy denial, got %v", err)
	}
}

func TestMemoryGetTool_RedactsAllowedSource(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "telegram", "users", "1001", "memory", "2026-04-30.md")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create fixture dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("api_key: sk-testsecret1234567890"), 0644); err != nil {
		t.Fatalf("failed to write fixture: %v", err)
	}

	policy := memory.NewRecallPolicy(memory.RecallContext{UserID: 1001, ChatID: 1001, ChatType: "private", SessionKey: "dm:1001"})
	tool := NewScopedMemoryGetTool(tmpDir, policy)
	got, err := tool.Execute(context.Background(), "telegram/users/1001/memory/2026-04-30.md")
	if err != nil {
		t.Fatalf("memory_get failed: %v", err)
	}
	if strings.Contains(got, "sk-testsecret") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redacted output, got %q", got)
	}
}
