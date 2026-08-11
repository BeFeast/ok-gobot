package bootstrap

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func TestSkillLibraryPassesStrictAudit(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	library := filepath.Join(root, "skills")
	entries, err := os.ReadDir(library)
	if err != nil {
		t.Fatalf("read skill library: %v", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		findings, err := AuditSkill(filepath.Join(library, name))
		if err != nil {
			t.Fatalf("audit %s: %v", name, err)
		}
		if AuditHasErrors(findings) {
			t.Fatalf("skill %s failed strict audit: %#v", name, findings)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	want := []string{"add-knowledge", "digest-curator", "issue-intake", "obsidian-markdown", "stem-separation", "transcript-summary"}
	if len(names) != len(want) {
		t.Fatalf("skill library = %#v, want %#v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("skill library = %#v, want %#v", names, want)
		}
	}
}
