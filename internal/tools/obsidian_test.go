package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObsidianToolKeepsExplicitExtensionsAndSupportsSearch(t *testing.T) {
	vault := t.TempDir()
	tool := NewObsidianTool(vault)

	if err := tool.WriteNote("routing/config.yaml", "routes:\n  ai: AI/Resources/Topics"); err != nil {
		t.Fatalf("WriteNote yaml: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "routing", "config.yaml")); err != nil {
		t.Fatalf("explicit extension was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "routing", "config.yaml.md")); !os.IsNotExist(err) {
		t.Fatalf("unexpected .md suffix file: %v", err)
	}
	yamlData, err := os.ReadFile(filepath.Join(vault, "routing", "config.yaml"))
	if err != nil {
		t.Fatalf("read yaml: %v", err)
	}
	if string(yamlData) != "routes:\n  ai: AI/Resources/Topics" {
		t.Fatalf("yaml content was rewritten: %q", yamlData)
	}

	if err := tool.WriteNote("Topics/agent-memory", "Agent memory notes"); err != nil {
		t.Fatalf("WriteNote markdown: %v", err)
	}
	result, err := tool.Execute(context.Background(), "search", "Agent memory")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(filepath.ToSlash(result), "Topics/agent-memory") {
		t.Fatalf("search result = %q", result)
	}
}

func TestObsidianToolRequiresExplicitVault(t *testing.T) {
	tool := NewObsidianTool("")
	if _, err := tool.ReadNote("note"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("ReadNote error = %v", err)
	}
}

func TestLoadFromConfigRegistersOnlyConfiguredObsidianVault(t *testing.T) {
	workspace := t.TempDir()
	registry, err := LoadFromConfigWithOptions(workspace, &ToolsConfig{})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions: %v", err)
	}
	if _, ok := registry.Get("obsidian"); ok {
		t.Fatal("obsidian tool registered without explicit vault")
	}

	vault := t.TempDir()
	registry, err = LoadFromConfigWithOptions(workspace, &ToolsConfig{ObsidianVaultDir: vault})
	if err != nil {
		t.Fatalf("LoadFromConfigWithOptions configured: %v", err)
	}
	registered, ok := registry.Get("obsidian")
	if !ok {
		t.Fatal("obsidian tool was not registered")
	}
	tool, ok := registered.(*ObsidianTool)
	if !ok || tool.VaultPath != vault {
		t.Fatalf("registered tool = %#v", registered)
	}
}
