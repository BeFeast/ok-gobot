// Package role implements markdown-first role manifests.
//
// Each role is a single markdown file with YAML frontmatter. The frontmatter
// carries structured metadata (worker, tools, schedule, report template,
// approval mode) while the markdown body is the role's system prompt.
//
// Example manifest:
//
//	---
//	worker: standard
//	tools: [web_fetch, search]
//	schedule: "0 9 * * *"
//	report_template: |
//	  ## {{.Title}}
//	  {{.Body}}
//	approval: auto
//	---
//	# Researcher
//	You are a research agent. Your job is to gather information...
package role

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"gopkg.in/yaml.v3"
)

// ApprovalMode controls when a role requires human approval for dangerous actions.
type ApprovalMode string

const (
	// ApprovalAuto uses the default heuristic (pattern-match dangerous commands).
	ApprovalAuto ApprovalMode = "auto"
	// ApprovalAlways requires approval for every tool call.
	ApprovalAlways ApprovalMode = "always"
	// ApprovalNever skips approval entirely (use with caution).
	ApprovalNever ApprovalMode = "never"
)

// validApprovalModes is the canonical set of recognised modes.
var validApprovalModes = map[ApprovalMode]struct{}{
	ApprovalAuto:   {},
	ApprovalAlways: {},
	ApprovalNever:  {},
}

// ValidApprovalMode reports whether m is a recognised approval mode.
func ValidApprovalMode(m ApprovalMode) bool {
	_, ok := validApprovalModes[m]
	return ok
}

// frontmatter is the YAML structure parsed from between the --- delimiters.
type frontmatter struct {
	Worker         string   `yaml:"worker"`
	Tools          []string `yaml:"tools"`
	Schedule       string   `yaml:"schedule"`
	ReportTemplate string   `yaml:"report_template"`
	Approval       string   `yaml:"approval"`
}

// Manifest is a parsed role definition loaded from a markdown file.
type Manifest struct {
	// Name is the role identifier, derived from the filename (without .md).
	Name string

	// Prompt is the markdown body after frontmatter — the role's system prompt.
	Prompt string

	// Worker selects the cost tier or worker adapter for this role.
	// Common values: "premium", "standard", "cheap", "local", or a custom adapter name.
	// Empty means the caller should use its default.
	Worker string

	// Tools lists the tool names this role is allowed to call.
	// An empty slice means all tools are allowed.
	Tools []string

	// Schedule is a cron expression for periodic execution.
	// Empty means the role is not scheduled.
	Schedule string

	// ReportTemplate is a Go text/template used to format the role's output.
	// Empty means raw output is used as-is.
	ReportTemplate string

	// Approval controls when the role requires human approval.
	// Defaults to ApprovalAuto when not specified.
	Approval ApprovalMode

	// SourcePath is the absolute path to the source .md file.
	SourcePath string
}

// Parse reads a role manifest from raw markdown bytes.
// The name argument is used as the role's identifier (typically the filename stem).
func Parse(name string, data []byte) (*Manifest, error) {
	fm, body, err := splitFrontmatter(data)
	if err != nil {
		return nil, fmt.Errorf("role %q: %w", name, err)
	}

	approval := ApprovalAuto
	if fm.Approval != "" {
		approval = ApprovalMode(strings.ToLower(strings.TrimSpace(fm.Approval)))
		if !ValidApprovalMode(approval) {
			return nil, fmt.Errorf("role %q: invalid approval mode %q (want auto, always, or never)", name, fm.Approval)
		}
	}

	m := &Manifest{
		Name:           name,
		Prompt:         strings.TrimSpace(body),
		Worker:         strings.TrimSpace(fm.Worker),
		Tools:          cleanTools(fm.Tools),
		Schedule:       strings.TrimSpace(fm.Schedule),
		ReportTemplate: fm.ReportTemplate,
		Approval:       approval,
	}

	if err := m.Validate(); err != nil {
		return nil, err
	}

	return m, nil
}

// Validate checks that the manifest fields are internally consistent.
func (m *Manifest) Validate() error {
	if m.Name == "" {
		return fmt.Errorf("role manifest: name is required")
	}

	if m.ReportTemplate != "" {
		if _, err := template.New("report").Parse(m.ReportTemplate); err != nil {
			return fmt.Errorf("role %q: invalid report_template: %w", m.Name, err)
		}
	}

	return nil
}

// HasSchedule reports whether this role defines a cron schedule.
func (m *Manifest) HasSchedule() bool {
	return m.Schedule != ""
}

// HasToolRestrictions reports whether this role restricts available tools.
func (m *Manifest) HasToolRestrictions() bool {
	return len(m.Tools) > 0
}

// IsToolAllowed reports whether toolName is permitted for this role.
// Returns true if there are no restrictions (empty Tools list).
func (m *Manifest) IsToolAllowed(toolName string) bool {
	if !m.HasToolRestrictions() {
		return true
	}
	for _, t := range m.Tools {
		if t == toolName {
			return true
		}
	}
	return false
}

// RenderReport executes the report template against data and returns the result.
// If no template is set, it returns an empty string and no error.
func (m *Manifest) RenderReport(data any) (string, error) {
	if m.ReportTemplate == "" {
		return "", nil
	}

	tmpl, err := template.New("report").Parse(m.ReportTemplate)
	if err != nil {
		return "", fmt.Errorf("role %q: report template parse error: %w", m.Name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("role %q: report template execute error: %w", m.Name, err)
	}

	return buf.String(), nil
}

// splitFrontmatter separates YAML frontmatter from the markdown body.
// Frontmatter must be delimited by "---" lines at the start of the document.
func splitFrontmatter(data []byte) (frontmatter, string, error) {
	var fm frontmatter

	text := string(data)
	trimmed := strings.TrimLeftFunc(text, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})

	// No frontmatter — the entire document is the prompt body.
	if !strings.HasPrefix(trimmed, "---") {
		return fm, text, nil
	}

	// Find the closing "---".
	rest := trimmed[3:] // skip opening "---"
	rest = strings.TrimLeft(rest, " \t")
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	} else if len(rest) > 1 && rest[0] == '\r' && rest[1] == '\n' {
		rest = rest[2:]
	}

	// Handle empty frontmatter: rest starts with "---" immediately.
	var yamlBlock string
	var body string
	if strings.HasPrefix(rest, "---") {
		yamlBlock = ""
		body = rest[3:]
	} else {
		closingIdx := strings.Index(rest, "\n---")
		if closingIdx < 0 {
			// No closing delimiter — treat entire doc as body (no frontmatter).
			return fm, text, nil
		}
		yamlBlock = rest[:closingIdx]
		body = rest[closingIdx+4:] // skip "\n---"
	}
	// Trim the line break after closing ---
	if len(body) > 0 && body[0] == '\n' {
		body = body[1:]
	} else if len(body) > 1 && body[0] == '\r' && body[1] == '\n' {
		body = body[2:]
	}

	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return fm, "", fmt.Errorf("invalid frontmatter YAML: %w", err)
	}

	return fm, body, nil
}

// cleanTools trims whitespace and removes empty entries from a tool list.
func cleanTools(tools []string) []string {
	var out []string
	for _, t := range tools {
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
