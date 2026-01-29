package tools

import (
	"testing"
)

func TestLoadFromConfig_RegistersNewTools(t *testing.T) {
	tmpDir := t.TempDir()

	registry, err := LoadFromConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromConfig failed: %v", err)
	}

	// Check if PatchTool is registered
	patchTool, ok := registry.Get("patch")
	if !ok {
		t.Error("PatchTool not registered")
	} else {
		if patchTool.Name() != "patch" {
			t.Errorf("Expected tool name 'patch', got %q", patchTool.Name())
		}
		if patchTool.Description() == "" {
			t.Error("PatchTool has empty description")
		}
	}

	// Check if SearchFileTool is registered
	grepTool, ok := registry.Get("grep")
	if !ok {
		t.Error("SearchFileTool not registered")
	} else {
		if grepTool.Name() != "grep" {
			t.Errorf("Expected tool name 'grep', got %q", grepTool.Name())
		}
		if grepTool.Description() == "" {
			t.Error("SearchFileTool has empty description")
		}
	}

	// Verify both tools are in the list
	allTools := registry.List()
	foundPatch := false
	foundGrep := false

	for _, tool := range allTools {
		if tool.Name() == "patch" {
			foundPatch = true
		}
		if tool.Name() == "grep" {
			foundGrep = true
		}
	}

	if !foundPatch {
		t.Error("PatchTool not found in registry list")
	}
	if !foundGrep {
		t.Error("SearchFileTool not found in registry list")
	}

	t.Logf("Successfully registered %d tools", len(allTools))
}
