package bootstrap

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveSkillVersion(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}

	skillFile := filepath.Join(skillDir, "SKILL.md")
	content := "# My Skill\n\nOriginal content.\n"
	if err := os.WriteFile(skillFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SaveSkillVersion(skillFile, 10); err != nil {
		t.Fatalf("SaveSkillVersion: %v", err)
	}

	versionsDir := filepath.Join(skillDir, ".versions")
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		t.Fatalf("versions dir not created: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 version file, got %d", len(entries))
	}

	saved, err := os.ReadFile(filepath.Join(versionsDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != content {
		t.Errorf("saved content mismatch: got %q want %q", string(saved), content)
	}
}

func TestSaveSkillVersion_NonexistentFile(t *testing.T) {
	dir := t.TempDir()
	nonexistent := filepath.Join(dir, "SKILL.md")
	// Should succeed silently with no backup created
	if err := SaveSkillVersion(nonexistent, 10); err != nil {
		t.Fatalf("unexpected error for nonexistent file: %v", err)
	}
	versionsDir := filepath.Join(dir, ".versions")
	if _, err := os.Stat(versionsDir); !os.IsNotExist(err) {
		t.Error("versions dir should not be created for nonexistent source")
	}
}

func TestSaveSkillVersion_Pruning(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "prune-test")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}

	skillFile := filepath.Join(skillDir, "SKILL.md")
	maxVersions := 3

	// Save more versions than the limit (no sleep needed; microsecond precision ensures unique filenames)
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(skillFile, []byte("version"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := SaveSkillVersion(skillFile, maxVersions); err != nil {
			t.Fatalf("SaveSkillVersion iteration %d: %v", i, err)
		}
	}

	versions, err := ListSkillVersions(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != maxVersions {
		t.Errorf("expected %d versions after pruning, got %d", maxVersions, len(versions))
	}
}

func TestListSkillVersions_Empty(t *testing.T) {
	dir := t.TempDir()
	versions, err := ListSkillVersions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 0 {
		t.Errorf("expected 0 versions, got %d", len(versions))
	}
}

func TestListSkillVersions_Ordering(t *testing.T) {
	dir := t.TempDir()
	versionsDir := filepath.Join(dir, ".versions")
	if err := os.MkdirAll(versionsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create version files out of order
	names := []string{
		"SKILL.md.v20240103000000",
		"SKILL.md.v20240101000000",
		"SKILL.md.v20240102000000",
	}
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(versionsDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	versions, err := ListSkillVersions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 3 {
		t.Fatalf("expected 3 versions, got %d", len(versions))
	}
	// Should be newest first
	if versions[0].Filename != "SKILL.md.v20240103000000" {
		t.Errorf("expected newest first, got %s", versions[0].Filename)
	}
	if versions[2].Filename != "SKILL.md.v20240101000000" {
		t.Errorf("expected oldest last, got %s", versions[2].Filename)
	}
}

func TestRollbackSkillVersion(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skills", "rollback-test")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}

	skillFile := filepath.Join(skillDir, "SKILL.md")
	original := "# Original\n"
	if err := os.WriteFile(skillFile, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SaveSkillVersion(skillFile, 10); err != nil {
		t.Fatal(err)
	}

	// Modify the skill
	if err := os.WriteFile(skillFile, []byte("# Modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	versions, err := ListSkillVersions(skillDir)
	if err != nil || len(versions) == 0 {
		t.Fatal("expected at least one version")
	}

	if err := RollbackSkillVersion(skillDir, versions[0].Filename); err != nil {
		t.Fatalf("RollbackSkillVersion: %v", err)
	}

	restored, err := os.ReadFile(skillFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != original {
		t.Errorf("rollback content mismatch: got %q want %q", string(restored), original)
	}
}

func TestRollbackSkillVersion_NotFound(t *testing.T) {
	dir := t.TempDir()
	err := RollbackSkillVersion(dir, "SKILL.md.v99991231235959")
	if err == nil {
		t.Error("expected error for nonexistent version")
	}
}
