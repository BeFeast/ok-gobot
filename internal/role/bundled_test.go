package role

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundledNames(t *testing.T) {
	names := BundledNames()
	if len(names) < 3 {
		t.Fatalf("BundledNames() = %v, want at least 3 bundled roles", names)
	}

	want := map[string]bool{
		"researcher":    false,
		"monitor":       false,
		"release-watch": false,
	}
	for _, n := range names {
		want[n] = true
	}
	for name, found := range want {
		if !found {
			t.Errorf("bundled role %q not found in BundledNames()", name)
		}
	}
}

func TestLoadBundled(t *testing.T) {
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled() failed: %v", err)
	}
	if len(manifests) < 3 {
		t.Fatalf("LoadBundled() returned %d manifests, want at least 3", len(manifests))
	}

	byName := make(map[string]*Manifest, len(manifests))
	for _, m := range manifests {
		byName[m.Name] = m
	}

	// Researcher
	r := byName["researcher"]
	if r == nil {
		t.Fatal("bundled role 'researcher' not found")
	}
	if r.Worker == "" {
		t.Error("researcher.Worker should be set")
	}
	if !r.HasSchedule() {
		t.Error("researcher should have a schedule")
	}
	if r.Prompt == "" {
		t.Error("researcher.Prompt should be non-empty")
	}

	// Monitor
	mon := byName["monitor"]
	if mon == nil {
		t.Fatal("bundled role 'monitor' not found")
	}
	if !mon.HasSchedule() {
		t.Error("monitor should have a schedule")
	}

	// Release-watch
	rw := byName["release-watch"]
	if rw == nil {
		t.Fatal("bundled role 'release-watch' not found")
	}
	if !rw.HasSchedule() {
		t.Error("release-watch should have a schedule")
	}
}

func TestWriteBundledTo(t *testing.T) {
	dir := t.TempDir()

	written, err := WriteBundledTo(dir, false)
	if err != nil {
		t.Fatalf("WriteBundledTo failed: %v", err)
	}
	if len(written) < 3 {
		t.Fatalf("WriteBundledTo wrote %d files, want at least 3", len(written))
	}

	// Verify files exist and are parseable.
	for _, path := range written {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("reading written file %s: %v", path, err)
			continue
		}
		name := filepath.Base(path)
		name = name[:len(name)-3] // strip .md
		if _, err := Parse(name, data); err != nil {
			t.Errorf("parsing written file %s: %v", path, err)
		}
	}
}

func TestWriteBundledTo_SkipsExisting(t *testing.T) {
	dir := t.TempDir()

	// Write once.
	written1, err := WriteBundledTo(dir, false)
	if err != nil {
		t.Fatalf("first WriteBundledTo failed: %v", err)
	}

	// Write again without overwrite — should skip all.
	written2, err := WriteBundledTo(dir, false)
	if err != nil {
		t.Fatalf("second WriteBundledTo failed: %v", err)
	}
	if len(written2) != 0 {
		t.Errorf("second WriteBundledTo wrote %d files (want 0, all should be skipped)", len(written2))
	}
	_ = written1
}

func TestWriteBundledTo_Overwrite(t *testing.T) {
	dir := t.TempDir()

	if _, err := WriteBundledTo(dir, false); err != nil {
		t.Fatalf("first WriteBundledTo failed: %v", err)
	}

	written, err := WriteBundledTo(dir, true)
	if err != nil {
		t.Fatalf("overwrite WriteBundledTo failed: %v", err)
	}
	if len(written) < 3 {
		t.Errorf("overwrite WriteBundledTo wrote %d files, want at least 3", len(written))
	}
}
