package role

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParse_FullManifest(t *testing.T) {
	data := []byte(`---
worker: premium
tools: [web_fetch, search, memory_search]
schedule: "0 9 * * *"
report_template: |
  ## {{.Title}}
  {{.Body}}
approval: always
---
# Researcher

You are a research agent. Your job is to gather information and compile reports.
`)

	m, err := Parse("researcher", data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if m.Name != "researcher" {
		t.Errorf("Name = %q, want %q", m.Name, "researcher")
	}
	if m.Worker != "premium" {
		t.Errorf("Worker = %q, want %q", m.Worker, "premium")
	}
	if len(m.Tools) != 3 {
		t.Fatalf("Tools length = %d, want 3", len(m.Tools))
	}
	if m.Tools[0] != "web_fetch" || m.Tools[1] != "search" || m.Tools[2] != "memory_search" {
		t.Errorf("Tools = %v, want [web_fetch search memory_search]", m.Tools)
	}
	if m.Schedule != "0 9 * * *" {
		t.Errorf("Schedule = %q, want %q", m.Schedule, "0 9 * * *")
	}
	if m.Approval != ApprovalAlways {
		t.Errorf("Approval = %q, want %q", m.Approval, ApprovalAlways)
	}
	if m.ReportTemplate == "" {
		t.Error("ReportTemplate is empty, want non-empty")
	}
	if !m.HasSchedule() {
		t.Error("HasSchedule() = false, want true")
	}
	if !m.HasToolRestrictions() {
		t.Error("HasToolRestrictions() = false, want true")
	}
	if m.IsToolAllowed("web_fetch") != true {
		t.Error("IsToolAllowed(web_fetch) = false, want true")
	}
	if m.IsToolAllowed("dangerous_exec") != false {
		t.Error("IsToolAllowed(dangerous_exec) = true, want false")
	}

	expected := "# Researcher\n\nYou are a research agent. Your job is to gather information and compile reports."
	if m.Prompt != expected {
		t.Errorf("Prompt = %q, want %q", m.Prompt, expected)
	}
}

func TestParse_MinimalManifest(t *testing.T) {
	data := []byte(`# Simple role

Just a prompt, no frontmatter at all.
`)

	m, err := Parse("simple", data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if m.Name != "simple" {
		t.Errorf("Name = %q, want %q", m.Name, "simple")
	}
	if m.Worker != "" {
		t.Errorf("Worker = %q, want empty", m.Worker)
	}
	if len(m.Tools) != 0 {
		t.Errorf("Tools = %v, want empty", m.Tools)
	}
	if m.Schedule != "" {
		t.Errorf("Schedule = %q, want empty", m.Schedule)
	}
	if m.Approval != ApprovalAuto {
		t.Errorf("Approval = %q, want %q", m.Approval, ApprovalAuto)
	}
	if m.HasSchedule() {
		t.Error("HasSchedule() = true, want false")
	}
	if m.HasToolRestrictions() {
		t.Error("HasToolRestrictions() = true, want false")
	}
	if m.IsToolAllowed("anything") != true {
		t.Error("IsToolAllowed(anything) = false, want true (no restrictions)")
	}
}

func TestParse_EmptyFrontmatter(t *testing.T) {
	data := []byte(`---
---
# Role with empty frontmatter

This should work fine.
`)

	m, err := Parse("empty-fm", data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if m.Name != "empty-fm" {
		t.Errorf("Name = %q, want %q", m.Name, "empty-fm")
	}
	if m.Approval != ApprovalAuto {
		t.Errorf("Approval = %q, want %q", m.Approval, ApprovalAuto)
	}
	expected := "# Role with empty frontmatter\n\nThis should work fine."
	if m.Prompt != expected {
		t.Errorf("Prompt = %q, want %q", m.Prompt, expected)
	}
}

func TestParse_InvalidApprovalMode(t *testing.T) {
	data := []byte(`---
approval: maybe
---
Some prompt.
`)

	_, err := Parse("bad-approval", data)
	if err == nil {
		t.Fatal("Parse should fail for invalid approval mode")
	}
}

func TestParse_InvalidReportTemplate(t *testing.T) {
	data := []byte(`---
report_template: "{{.Broken"
---
Some prompt.
`)

	_, err := Parse("bad-template", data)
	if err == nil {
		t.Fatal("Parse should fail for invalid report template")
	}
}

func TestParse_EmptyName(t *testing.T) {
	data := []byte(`Some prompt.`)

	_, err := Parse("", data)
	if err == nil {
		t.Fatal("Parse should fail for empty name")
	}
}

func TestParse_ApprovalModeNormalization(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  ApprovalMode
	}{
		{"auto", ApprovalAuto},
		{"AUTO", ApprovalAuto},
		{"  Always ", ApprovalAlways},
		{"never", ApprovalNever},
		{"NEVER", ApprovalNever},
	} {
		data := []byte("---\napproval: " + tc.input + "\n---\nPrompt.\n")
		m, err := Parse("test", data)
		if err != nil {
			t.Errorf("Parse(approval=%q) failed: %v", tc.input, err)
			continue
		}
		if m.Approval != tc.want {
			t.Errorf("approval=%q → %q, want %q", tc.input, m.Approval, tc.want)
		}
	}
}

func TestManifest_RenderReport(t *testing.T) {
	m := &Manifest{
		Name:           "test",
		ReportTemplate: "## {{.Title}}\n{{.Body}}",
	}

	data := map[string]string{
		"Title": "Daily Report",
		"Body":  "All systems operational.",
	}

	result, err := m.RenderReport(data)
	if err != nil {
		t.Fatalf("RenderReport failed: %v", err)
	}

	expected := "## Daily Report\nAll systems operational."
	if result != expected {
		t.Errorf("RenderReport = %q, want %q", result, expected)
	}
}

func TestManifest_RenderReport_Empty(t *testing.T) {
	m := &Manifest{Name: "test"}
	result, err := m.RenderReport(nil)
	if err != nil {
		t.Fatalf("RenderReport failed: %v", err)
	}
	if result != "" {
		t.Errorf("RenderReport = %q, want empty", result)
	}
}

func TestValidApprovalMode(t *testing.T) {
	if !ValidApprovalMode(ApprovalAuto) {
		t.Error("ApprovalAuto should be valid")
	}
	if !ValidApprovalMode(ApprovalAlways) {
		t.Error("ApprovalAlways should be valid")
	}
	if !ValidApprovalMode(ApprovalNever) {
		t.Error("ApprovalNever should be valid")
	}
	if ValidApprovalMode("maybe") {
		t.Error("'maybe' should not be valid")
	}
}

func TestLoadDir(t *testing.T) {
	dir := t.TempDir()

	// Write two valid role files.
	writeFile(t, dir, "monitor.md", `---
worker: cheap
schedule: "*/15 * * * *"
tools: [web_fetch]
---
# Monitor
Check service health.
`)

	writeFile(t, dir, "reporter.md", `---
worker: standard
schedule: "0 9 * * *"
approval: never
---
# Reporter
Generate daily summaries.
`)

	// Write a non-md file that should be ignored.
	writeFile(t, dir, "README.txt", "not a role")

	manifests, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir failed: %v", err)
	}

	if len(manifests) != 2 {
		t.Fatalf("LoadDir returned %d manifests, want 2", len(manifests))
	}

	// Results should be sorted by name.
	if manifests[0].Name != "monitor" {
		t.Errorf("manifests[0].Name = %q, want %q", manifests[0].Name, "monitor")
	}
	if manifests[1].Name != "reporter" {
		t.Errorf("manifests[1].Name = %q, want %q", manifests[1].Name, "reporter")
	}

	// Verify fields on first manifest.
	m := manifests[0]
	if m.Worker != "cheap" {
		t.Errorf("monitor.Worker = %q, want %q", m.Worker, "cheap")
	}
	if m.Schedule != "*/15 * * * *" {
		t.Errorf("monitor.Schedule = %q, want %q", m.Schedule, "*/15 * * * *")
	}
	if len(m.Tools) != 1 || m.Tools[0] != "web_fetch" {
		t.Errorf("monitor.Tools = %v, want [web_fetch]", m.Tools)
	}
	if m.SourcePath != filepath.Join(dir, "monitor.md") {
		t.Errorf("monitor.SourcePath = %q, want %q", m.SourcePath, filepath.Join(dir, "monitor.md"))
	}

	// Verify second manifest.
	if manifests[1].Approval != ApprovalNever {
		t.Errorf("reporter.Approval = %q, want %q", manifests[1].Approval, ApprovalNever)
	}
}

func TestLoadDir_InvalidFile(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "bad.md", `---
approval: invalid_mode
---
Bad role.
`)

	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("LoadDir should fail with invalid manifest")
	}
}

func TestLoadDirLenient(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, dir, "good.md", `---
worker: standard
---
# Good role
Works fine.
`)

	writeFile(t, dir, "bad.md", `---
approval: invalid_mode
---
Bad role.
`)

	manifests, errs := LoadDirLenient(dir)
	if len(manifests) != 1 {
		t.Errorf("LoadDirLenient returned %d manifests, want 1", len(manifests))
	}
	if len(errs) != 1 {
		t.Errorf("LoadDirLenient returned %d errors, want 1", len(errs))
	}
	if len(manifests) > 0 && manifests[0].Name != "good" {
		t.Errorf("manifests[0].Name = %q, want %q", manifests[0].Name, "good")
	}
}

func TestLoadDir_Empty(t *testing.T) {
	dir := t.TempDir()

	manifests, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir failed on empty dir: %v", err)
	}
	if len(manifests) != 0 {
		t.Errorf("LoadDir returned %d manifests, want 0", len(manifests))
	}
}

func TestLoadDir_Missing(t *testing.T) {
	_, err := LoadDir("/nonexistent/path/that/does/not/exist")
	if err == nil {
		t.Fatal("LoadDir should fail on missing directory")
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ops.md")
	writeFile(t, dir, "ops.md", `---
worker: cheap
tools: [local_exec]
approval: always
---
# Ops
Run maintenance tasks.
`)

	m, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile failed: %v", err)
	}

	if m.Name != "ops" {
		t.Errorf("Name = %q, want %q", m.Name, "ops")
	}
	if m.Worker != "cheap" {
		t.Errorf("Worker = %q, want %q", m.Worker, "cheap")
	}
	if m.Approval != ApprovalAlways {
		t.Errorf("Approval = %q, want %q", m.Approval, ApprovalAlways)
	}
	if m.SourcePath != path {
		t.Errorf("SourcePath = %q, want %q", m.SourcePath, path)
	}
}

func TestParse_WorkerOnly(t *testing.T) {
	data := []byte(`---
worker: local
---
Run locally.
`)

	m, err := Parse("local-runner", data)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if m.Worker != "local" {
		t.Errorf("Worker = %q, want %q", m.Worker, "local")
	}
	if m.Schedule != "" {
		t.Errorf("Schedule = %q, want empty", m.Schedule)
	}
	if m.Prompt != "Run locally." {
		t.Errorf("Prompt = %q, want %q", m.Prompt, "Run locally.")
	}
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
