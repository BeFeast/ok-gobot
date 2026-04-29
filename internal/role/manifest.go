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
	"time"

	"ok-gobot/internal/delegation"

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
	MaxToolCalls   int      `yaml:"max_tool_calls"`
	MaxDuration    string   `yaml:"max_duration"`
	MaxTokens      int      `yaml:"max_tokens"`
	MaxCostUSD     float64  `yaml:"max_cost_usd"`
	MemoryPolicy   string   `yaml:"memory_policy"`
	Model          string   `yaml:"model"`
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

	// Budget fields for runtime enforcement.

	// MaxToolCalls limits the number of tool executions per run.
	// 0 means use the delegation default.
	MaxToolCalls int

	// MaxDuration limits wall-clock time per run.
	// 0 means use the delegation default.
	MaxDuration time.Duration

	// MaxTokens limits total tokens (prompt + completion) per run.
	// 0 means unlimited.
	MaxTokens int

	// MaxCostUSD limits estimated cost in USD per run.
	// 0 means unlimited.
	MaxCostUSD float64

	// MemoryPolicy controls memory write access.
	// Valid values: "inherit", "read_only", "allow_writes".
	// Empty means use the delegation default.
	MemoryPolicy string

	// Model overrides the AI model for this role.
	// Empty means the caller should use its default.
	Model string
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

	var maxDuration time.Duration
	if raw := strings.TrimSpace(fm.MaxDuration); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("role %q: invalid max_duration %q: %w", name, raw, err)
		}
		maxDuration = d
	}

	memoryPolicy := strings.TrimSpace(fm.MemoryPolicy)
	if memoryPolicy != "" {
		if _, ok := delegation.ParseMemoryPolicy(memoryPolicy); !ok {
			return nil, fmt.Errorf("role %q: invalid memory_policy %q (want inherit, read_only, or allow_writes)", name, memoryPolicy)
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
		MaxToolCalls:   fm.MaxToolCalls,
		MaxDuration:    maxDuration,
		MaxTokens:      fm.MaxTokens,
		MaxCostUSD:     fm.MaxCostUSD,
		MemoryPolicy:   memoryPolicy,
		Model:          strings.TrimSpace(fm.Model),
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

	if m.MaxToolCalls < 0 {
		return fmt.Errorf("role %q: max_tool_calls must be >= 0, got %d", m.Name, m.MaxToolCalls)
	}
	if m.MaxDuration < 0 {
		return fmt.Errorf("role %q: max_duration must be >= 0, got %s", m.Name, m.MaxDuration)
	}
	if m.MaxTokens < 0 {
		return fmt.Errorf("role %q: max_tokens must be >= 0, got %d", m.Name, m.MaxTokens)
	}
	if m.MaxCostUSD < 0 {
		return fmt.Errorf("role %q: max_cost_usd must be >= 0, got %f", m.Name, m.MaxCostUSD)
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

// ToDelegationJob builds a delegation.Job from the manifest's budget fields.
// Fields left at zero/empty use the delegation package defaults.
func (m *Manifest) ToDelegationJob() delegation.Job {
	return delegation.Job{
		Model:         m.Model,
		ToolAllowlist: m.Tools,
		MaxToolCalls:  m.MaxToolCalls,
		MaxDuration:   m.MaxDuration,
		MaxTokens:     m.MaxTokens,
		MaxCostUSD:    m.MaxCostUSD,
		MemoryPolicy:  m.MemoryPolicy,
	}
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
