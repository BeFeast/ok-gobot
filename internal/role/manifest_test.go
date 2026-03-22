package role

import (
	"testing"
)

func TestParseManifest_Full(t *testing.T) {
	input := `---
name: researcher
description: Researches topics and produces summaries
worker: sonnet
tools:
  - web_fetch
  - web_search
schedule: "0 9 * * 1-5"
report_template: |
  ## Report
  {{.Body}}
approval: auto
---

You are a research agent. Your job is to find information.
`
	m, err := ParseManifest(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.Name != "researcher" {
		t.Errorf("Name = %q, want %q", m.Name, "researcher")
	}
	if m.Description != "Researches topics and produces summaries" {
		t.Errorf("Description = %q, want %q", m.Description, "Researches topics and produces summaries")
	}
	if m.Worker != "sonnet" {
		t.Errorf("Worker = %q, want %q", m.Worker, "sonnet")
	}
	if len(m.Tools) != 2 || m.Tools[0] != "web_fetch" || m.Tools[1] != "web_search" {
		t.Errorf("Tools = %v, want [web_fetch web_search]", m.Tools)
	}
	if m.Schedule != "0 9 * * 1-5" {
		t.Errorf("Schedule = %q, want %q", m.Schedule, "0 9 * * 1-5")
	}
	if m.ReportTemplate == "" {
		t.Error("ReportTemplate is empty")
	}
	if m.Approval != ApprovalAuto {
		t.Errorf("Approval = %q, want %q", m.Approval, ApprovalAuto)
	}
	if m.Prompt != "You are a research agent. Your job is to find information." {
		t.Errorf("Prompt = %q", m.Prompt)
	}
}

func TestParseManifest_MinimalFields(t *testing.T) {
	input := `---
name: monitor
---

Watch for changes.`

	m, err := ParseManifest(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m.Name != "monitor" {
		t.Errorf("Name = %q, want %q", m.Name, "monitor")
	}
	if m.Prompt != "Watch for changes." {
		t.Errorf("Prompt = %q", m.Prompt)
	}
	if m.HasSchedule() {
		t.Error("HasSchedule should be false")
	}
	if m.HasTools() {
		t.Error("HasTools should be false")
	}
}

func TestParseManifest_MissingName(t *testing.T) {
	input := `---
description: no name
---

Some prompt.`

	_, err := ParseManifest(input)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestParseManifest_InvalidApproval(t *testing.T) {
	input := `---
name: test
approval: yolo
---

Some prompt.`

	_, err := ParseManifest(input)
	if err == nil {
		t.Fatal("expected error for invalid approval mode")
	}
}

func TestParseManifest_NoFrontmatter(t *testing.T) {
	input := `Just a plain markdown file.`

	_, err := ParseManifest(input)
	if err == nil {
		t.Fatal("expected error for missing name (no frontmatter)")
	}
}

func TestParseManifest_UnclosedFrontmatter(t *testing.T) {
	input := `---
name: broken
no closing delimiter`

	_, err := ParseManifest(input)
	if err == nil {
		t.Fatal("expected error for unclosed frontmatter")
	}
}

func TestParseManifest_EmptyContent(t *testing.T) {
	_, err := ParseManifest("")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
}

func TestEffectiveApproval_DefaultsToManual(t *testing.T) {
	m := &Manifest{Name: "test"}
	if m.EffectiveApproval() != ApprovalManual {
		t.Errorf("EffectiveApproval = %q, want %q", m.EffectiveApproval(), ApprovalManual)
	}
}

func TestEffectiveApproval_RespectsExplicit(t *testing.T) {
	m := &Manifest{Name: "test", Approval: ApprovalNone}
	if m.EffectiveApproval() != ApprovalNone {
		t.Errorf("EffectiveApproval = %q, want %q", m.EffectiveApproval(), ApprovalNone)
	}
}

func TestParseManifest_EmptyBody(t *testing.T) {
	input := `---
name: empty-body
---
`
	m, err := ParseManifest(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Prompt != "" {
		t.Errorf("Prompt = %q, want empty", m.Prompt)
	}
}

func TestApprovalModes(t *testing.T) {
	for _, mode := range []ApprovalMode{ApprovalAuto, ApprovalManual, ApprovalNone} {
		input := "---\nname: test\napproval: " + string(mode) + "\n---\n\nprompt"
		m, err := ParseManifest(input)
		if err != nil {
			t.Errorf("approval %q: unexpected error: %v", mode, err)
			continue
		}
		if m.Approval != mode {
			t.Errorf("Approval = %q, want %q", m.Approval, mode)
		}
	}
}
