package role

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoaderLoadAll(t *testing.T) {
	dir := t.TempDir()

	// Create two valid roles and one invalid.
	writeRole(t, dir, "alpha", `---
name: alpha
description: First role
tools:
  - web_fetch
schedule: "0 * * * *"
approval: auto
---

Alpha prompt.`)

	writeRole(t, dir, "beta", `---
name: beta
worker: haiku
---

Beta prompt.`)

	writeRole(t, dir, "broken", `---
description: missing name
---

No name here.`)

	loader := NewLoader(dir)
	manifests, errs := loader.LoadAll()

	if len(errs) != 1 {
		t.Errorf("expected 1 error, got %d: %v", len(errs), errs)
	}

	if len(manifests) != 2 {
		t.Fatalf("expected 2 manifests, got %d", len(manifests))
	}

	// Should be sorted by name.
	if manifests[0].Name != "alpha" {
		t.Errorf("first manifest Name = %q, want %q", manifests[0].Name, "alpha")
	}
	if manifests[1].Name != "beta" {
		t.Errorf("second manifest Name = %q, want %q", manifests[1].Name, "beta")
	}

	// Check source paths are set.
	if manifests[0].SourcePath == "" {
		t.Error("SourcePath not set on alpha")
	}
}

func TestLoaderLoadAll_NoRolesDir(t *testing.T) {
	dir := t.TempDir()

	loader := NewLoader(dir)
	manifests, errs := loader.LoadAll()

	if len(manifests) != 0 {
		t.Errorf("expected 0 manifests, got %d", len(manifests))
	}
	if len(errs) != 0 {
		t.Errorf("expected 0 errors, got %d", len(errs))
	}
}

func TestLoaderLoad_ByName(t *testing.T) {
	dir := t.TempDir()

	writeRole(t, dir, "monitor", `---
name: monitor
approval: none
---

Monitor prompt.`)

	loader := NewLoader(dir)
	m, err := loader.Load("monitor")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.Name != "monitor" {
		t.Errorf("Name = %q, want %q", m.Name, "monitor")
	}
	if m.Approval != ApprovalNone {
		t.Errorf("Approval = %q, want %q", m.Approval, ApprovalNone)
	}
}

func TestLoaderLoad_NotFound(t *testing.T) {
	dir := t.TempDir()

	loader := NewLoader(dir)
	_, err := loader.Load("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
}

func TestLoaderLoad_PathTraversal(t *testing.T) {
	dir := t.TempDir()

	loader := NewLoader(dir)
	_, err := loader.Load("../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestLoaderLoadAll_SkipsFiles(t *testing.T) {
	dir := t.TempDir()

	rolesDir := filepath.Join(dir, "roles")
	if err := os.MkdirAll(rolesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create a file (not a directory) in roles/.
	if err := os.WriteFile(filepath.Join(rolesDir, "README.md"), []byte("# Roles"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeRole(t, dir, "valid", `---
name: valid
---

Valid prompt.`)

	loader := NewLoader(dir)
	manifests, errs := loader.LoadAll()

	if len(manifests) != 1 {
		t.Errorf("expected 1 manifest, got %d", len(manifests))
	}
	if len(errs) != 0 {
		t.Errorf("expected 0 errors, got %v", errs)
	}
}

func writeRole(t *testing.T, baseDir, name, content string) {
	t.Helper()
	roleDir := filepath.Join(baseDir, "roles", name)
	if err := os.MkdirAll(roleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(roleDir, ManifestFile), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
