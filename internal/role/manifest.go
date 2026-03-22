// Package role implements markdown-first role manifests.
//
// A role manifest is a markdown file with YAML frontmatter that declaratively
// defines an autonomous agent role — its prompt, worker model, tools, schedule,
// report template, and approval mode.
//
// Example manifest (roles/researcher/ROLE.md):
//
//	---
//	name: researcher
//	description: Researches topics and produces summaries
//	worker: sonnet
//	tools:
//	  - web_fetch
//	  - web_search
//	schedule: "0 9 * * 1-5"
//	report_template: |
//	  ## {{.Name}} Report
//	  {{.Body}}
//	approval: auto
//	---
//
//	You are a research agent. Your job is to ...
package role

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ApprovalMode controls how a role's actions are approved.
type ApprovalMode string

const (
	// ApprovalAuto means the role runs without human approval.
	ApprovalAuto ApprovalMode = "auto"
	// ApprovalManual means every action requires explicit human approval.
	ApprovalManual ApprovalMode = "manual"
	// ApprovalNone means the role has no approval gate (fire-and-forget).
	ApprovalNone ApprovalMode = "none"
)

// Manifest represents a parsed role manifest.
type Manifest struct {
	// Name is the role identifier (e.g. "researcher", "monitor").
	Name string `yaml:"name"`
	// Description is a short human-readable summary.
	Description string `yaml:"description"`
	// Worker specifies the model or worker type (e.g. "sonnet", "haiku").
	Worker string `yaml:"worker"`
	// Tools lists the tool names this role is allowed to use.
	Tools []string `yaml:"tools"`
	// Schedule is a cron expression for periodic execution.
	Schedule string `yaml:"schedule"`
	// ReportTemplate is a Go text/template for formatting output.
	ReportTemplate string `yaml:"report_template"`
	// Approval controls the approval mode for this role's actions.
	Approval ApprovalMode `yaml:"approval"`

	// Prompt is the markdown body after frontmatter — the role's system prompt.
	Prompt string `yaml:"-"`
	// SourcePath is the file path this manifest was loaded from.
	SourcePath string `yaml:"-"`
}

// ParseManifest parses a markdown-first role manifest from raw content.
// The file format is YAML frontmatter delimited by "---" lines, followed by
// a markdown body that becomes the role's prompt.
func ParseManifest(content string) (*Manifest, error) {
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}

	var m Manifest
	if frontmatter != "" {
		if err := yaml.Unmarshal([]byte(frontmatter), &m); err != nil {
			return nil, fmt.Errorf("invalid frontmatter YAML: %w", err)
		}
	}

	m.Prompt = body

	if err := m.validate(); err != nil {
		return nil, err
	}

	return &m, nil
}

// validate checks that the manifest has the minimum required fields.
func (m *Manifest) validate() error {
	if m.Name == "" {
		return fmt.Errorf("role manifest: name is required")
	}

	if m.Approval != "" {
		switch m.Approval {
		case ApprovalAuto, ApprovalManual, ApprovalNone:
			// valid
		default:
			return fmt.Errorf("role manifest %q: invalid approval mode %q (must be auto, manual, or none)", m.Name, m.Approval)
		}
	}

	return nil
}

// HasSchedule reports whether the role defines a cron schedule.
func (m *Manifest) HasSchedule() bool {
	return m.Schedule != ""
}

// HasTools reports whether the role restricts its tool set.
func (m *Manifest) HasTools() bool {
	return len(m.Tools) > 0
}

// EffectiveApproval returns the approval mode, defaulting to "manual" if unset.
func (m *Manifest) EffectiveApproval() ApprovalMode {
	if m.Approval == "" {
		return ApprovalManual
	}
	return m.Approval
}

// splitFrontmatter splits a markdown document into YAML frontmatter and body.
// Returns ("", body, nil) if no frontmatter is present.
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", "", fmt.Errorf("empty manifest")
	}

	// Frontmatter must start with "---" on the first line.
	if !strings.HasPrefix(content, "---") {
		return "", content, nil
	}

	// Find the closing "---".
	rest := content[3:]
	// Skip the newline after the opening ---.
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}

	closingIdx := strings.Index(rest, "\n---")
	if closingIdx < 0 {
		return "", "", fmt.Errorf("unclosed frontmatter: missing closing ---")
	}

	frontmatter = rest[:closingIdx]

	// Body starts after the closing "---" line.
	afterClosing := rest[closingIdx+4:] // len("\n---") == 4
	// Skip optional newline after closing ---.
	if len(afterClosing) > 0 && afterClosing[0] == '\n' {
		afterClosing = afterClosing[1:]
	} else if len(afterClosing) > 1 && afterClosing[0] == '\r' && afterClosing[1] == '\n' {
		afterClosing = afterClosing[2:]
	}

	body = strings.TrimSpace(afterClosing)
	return frontmatter, body, nil
}
