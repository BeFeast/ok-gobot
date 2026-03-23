package role

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundledNames(t *testing.T) {
	names, err := BundledNames()
	if err != nil {
		t.Fatalf("BundledNames: %v", err)
	}

	want := []string{"monitor", "release-watch", "researcher"}
	if len(names) != len(want) {
		t.Fatalf("BundledNames returned %d names, want %d: %v", len(names), len(want), names)
	}
	for i, name := range names {
		if name != want[i] {
			t.Errorf("BundledNames[%d] = %q, want %q", i, name, want[i])
		}
	}
}

func TestLoadBundled(t *testing.T) {
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled: %v", err)
	}

	if len(manifests) != 3 {
		t.Fatalf("LoadBundled returned %d manifests, want 3", len(manifests))
	}

	// Verify sorted order
	for i := 1; i < len(manifests); i++ {
		if manifests[i].Name < manifests[i-1].Name {
			t.Errorf("manifests not sorted: %q before %q", manifests[i-1].Name, manifests[i].Name)
		}
	}

	// Check each manifest has required fields
	for _, m := range manifests {
		if m.Prompt == "" {
			t.Errorf("role %q: empty prompt", m.Name)
		}
		if !m.HasSchedule() {
			t.Errorf("role %q: expected a schedule", m.Name)
		}
		if m.Worker == "" {
			t.Errorf("role %q: expected a worker tier", m.Name)
		}
		if !m.HasToolRestrictions() {
			t.Errorf("role %q: expected tool restrictions", m.Name)
		}
	}
}

func TestLoadBundled_ReportTemplatesValid(t *testing.T) {
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled: %v", err)
	}

	type reportData struct {
		Title string
		Body  string
	}

	for _, m := range manifests {
		if m.ReportTemplate == "" {
			t.Errorf("role %q: no report template", m.Name)
			continue
		}

		rendered, err := m.RenderReport(reportData{
			Title: "Test Report",
			Body:  "Test body content.",
		})
		if err != nil {
			t.Errorf("role %q: RenderReport failed: %v", m.Name, err)
			continue
		}
		if rendered == "" {
			t.Errorf("role %q: RenderReport returned empty string", m.Name)
		}
	}
}

func TestLoadBundled_PromptsBounded(t *testing.T) {
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled: %v", err)
	}

	const maxPromptLen = 3000 // keep prompts bounded for Telegram readability
	for _, m := range manifests {
		if len(m.Prompt) > maxPromptLen {
			t.Errorf("role %q: prompt length %d exceeds %d", m.Name, len(m.Prompt), maxPromptLen)
		}
	}
}

func TestScaffold(t *testing.T) {
	dir := t.TempDir()
	rolesDir := filepath.Join(dir, "roles")

	// First scaffold — all files should be written
	written, err := Scaffold(rolesDir)
	if err != nil {
		t.Fatalf("Scaffold: %v", err)
	}

	names, _ := BundledNames()
	if len(written) != len(names) {
		t.Fatalf("Scaffold wrote %d files, want %d", len(written), len(names))
	}

	// Verify files exist and are parseable
	for _, name := range names {
		path := filepath.Join(rolesDir, name+".md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %s to exist", path)
			continue
		}

		m, err := LoadFile(path)
		if err != nil {
			t.Errorf("LoadFile(%s): %v", path, err)
		}
		if m.Name != name {
			t.Errorf("loaded name = %q, want %q", m.Name, name)
		}
	}

	// Second scaffold — nothing should be written (no overwrite)
	written2, err := Scaffold(rolesDir)
	if err != nil {
		t.Fatalf("Scaffold (second run): %v", err)
	}
	if len(written2) != 0 {
		t.Errorf("second Scaffold wrote %d files, want 0 (no overwrite)", len(written2))
	}
}
