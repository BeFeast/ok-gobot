package role

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundledNames(t *testing.T) {
	names := BundledNames()
	if len(names) < 4 {
		t.Fatalf("BundledNames() = %v, want at least 4 bundled roles", names)
	}

	want := map[string]bool{
		"researcher":      false,
		"monitor":         false,
		"release-watch":   false,
		"homelab-runbook": false,
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
	if len(manifests) < 4 {
		t.Fatalf("LoadBundled() returned %d manifests, want at least 4", len(manifests))
	}

	byName := make(map[string]*Manifest, len(manifests))
	for _, m := range manifests {
		byName[m.Name] = m
	}

	// Researcher — manual only (no schedule).
	r := byName["researcher"]
	if r == nil {
		t.Fatal("bundled role 'researcher' not found")
	}
	if r.Worker == "" {
		t.Error("researcher.Worker should be set")
	}
	if r.HasSchedule() {
		t.Error("researcher should be manual-only (no schedule)")
	}
	if r.Prompt == "" {
		t.Error("researcher.Prompt should be non-empty")
	}

	// Monitor — has a default schedule.
	mon := byName["monitor"]
	if mon == nil {
		t.Fatal("bundled role 'monitor' not found")
	}
	if !mon.HasSchedule() {
		t.Error("monitor should have a schedule")
	}

	// Release-watch — has a default schedule.
	rw := byName["release-watch"]
	if rw == nil {
		t.Fatal("bundled role 'release-watch' not found")
	}
	if !rw.HasSchedule() {
		t.Error("release-watch should have a schedule")
	}

	// Homelab-runbook — manual only (no schedule).
	hr := byName["homelab-runbook"]
	if hr == nil {
		t.Fatal("bundled role 'homelab-runbook' not found")
	}
	if hr.HasSchedule() {
		t.Error("homelab-runbook should be manual-only (no schedule)")
	}
}

func TestLoadBundled_AllHaveBudget(t *testing.T) {
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled() failed: %v", err)
	}
	for _, m := range manifests {
		if m.Budget <= 0 {
			t.Errorf("bundled role %q has no budget (got %d)", m.Name, m.Budget)
		}
	}
}

func TestLoadBundled_AllHaveReportTemplate(t *testing.T) {
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled() failed: %v", err)
	}
	for _, m := range manifests {
		if m.ReportTemplate == "" {
			t.Errorf("bundled role %q has no report_template", m.Name)
		}
	}
}

func TestLoadBundled_AllHaveToolRestrictions(t *testing.T) {
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled() failed: %v", err)
	}
	for _, m := range manifests {
		if !m.HasToolRestrictions() {
			t.Errorf("bundled role %q has no tool restrictions", m.Name)
		}
	}
}

func TestLoadBundled_ToolRestrictions(t *testing.T) {
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled() failed: %v", err)
	}

	byName := make(map[string]*Manifest, len(manifests))
	for _, m := range manifests {
		byName[m.Name] = m
	}

	// Monitor should only allow web_fetch.
	mon := byName["monitor"]
	if mon.IsToolAllowed("web_fetch") != true {
		t.Error("monitor should allow web_fetch")
	}
	if mon.IsToolAllowed("local") != false {
		t.Error("monitor should not allow local by default")
	}
	if mon.IsToolAllowed("search") != false {
		t.Error("monitor should not allow search")
	}

	// Researcher should allow search, web_fetch, memory_search.
	r := byName["researcher"]
	for _, tool := range []string{"search", "web_fetch", "memory_search"} {
		if !r.IsToolAllowed(tool) {
			t.Errorf("researcher should allow %s", tool)
		}
	}
	if r.IsToolAllowed("local") {
		t.Error("researcher should not allow local")
	}

	// Release-watch should allow web_fetch, search, memory_get, memory_search.
	rw := byName["release-watch"]
	for _, tool := range []string{"web_fetch", "search", "memory_get", "memory_search"} {
		if !rw.IsToolAllowed(tool) {
			t.Errorf("release-watch should allow %s", tool)
		}
	}
	if rw.IsToolAllowed("local") {
		t.Error("release-watch should not allow local")
	}

	// Homelab-runbook should allow obsidian, memory_get, memory_search.
	hr := byName["homelab-runbook"]
	for _, tool := range []string{"obsidian", "memory_get", "memory_search"} {
		if !hr.IsToolAllowed(tool) {
			t.Errorf("homelab-runbook should allow %s", tool)
		}
	}
	if hr.IsToolAllowed("local") {
		t.Error("homelab-runbook should not allow local")
	}
}

func TestLoadBundled_ReportTemplatesRender(t *testing.T) {
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled() failed: %v", err)
	}

	data := map[string]string{
		"Summary": "Test summary content.",
		"Date":    "2026-04-29",
	}

	for _, m := range manifests {
		result, err := m.RenderReport(data)
		if err != nil {
			t.Errorf("RenderReport for %q failed: %v", m.Name, err)
			continue
		}
		if result == "" {
			t.Errorf("RenderReport for %q returned empty string", m.Name)
		}
	}
}

func TestLoadBundled_DisabledByDefault(t *testing.T) {
	// Bundled roles are templates — they are NOT automatically registered
	// into the cron system. Only roles copied to the operator's roles_path
	// and loaded via LoadDir are eligible for RegisterRoleJobs.
	//
	// Manual-only roles (researcher, homelab-runbook) have no schedule,
	// so even if loaded they won't be registered.
	manifests, err := LoadBundled()
	if err != nil {
		t.Fatalf("LoadBundled() failed: %v", err)
	}

	manualOnly := map[string]bool{"researcher": true, "homelab-runbook": true}
	for _, m := range manifests {
		if manualOnly[m.Name] && m.HasSchedule() {
			t.Errorf("manual-only role %q should have no schedule", m.Name)
		}
	}
}

func TestWriteBundledTo(t *testing.T) {
	dir := t.TempDir()

	written, err := WriteBundledTo(dir, false)
	if err != nil {
		t.Fatalf("WriteBundledTo failed: %v", err)
	}
	if len(written) < 4 {
		t.Fatalf("WriteBundledTo wrote %d files, want at least 4", len(written))
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
	if len(written) < 4 {
		t.Errorf("overwrite WriteBundledTo wrote %d files, want at least 4", len(written))
	}
}
