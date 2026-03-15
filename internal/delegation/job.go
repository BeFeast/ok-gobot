package delegation

import (
	"fmt"
	"strings"
	"time"
)

const (
	DefaultMaxToolCalls = 50
	DefaultMaxDuration  = 10 * time.Minute

	OutputFormatText     = "text"
	OutputFormatMarkdown = "markdown"
	OutputFormatJSON     = "json"

	MemoryPolicyInherit     = "inherit"
	MemoryPolicyReadOnly    = "read_only"
	MemoryPolicyAllowWrites = "allow_writes"
)

// Job describes the explicit contract for a delegated run.
type Job struct {
	Model         string
	Thinking      string
	ToolAllowlist []string
	WorkspaceRoot string
	MaxToolCalls  int
	MaxDuration   time.Duration
	OutputFormat  string
	OutputSchema  string
	MemoryPolicy  string
}

// WithDefaults returns a normalized copy with stable defaults.
func (j Job) WithDefaults() Job {
	if j.MaxToolCalls <= 0 {
		j.MaxToolCalls = DefaultMaxToolCalls
	}
	if j.MaxDuration <= 0 {
		j.MaxDuration = DefaultMaxDuration
	}
	j.OutputFormat = NormalizeOutputFormat(j.OutputFormat)
	j.MemoryPolicy = NormalizeMemoryPolicy(j.MemoryPolicy)
	j.Model = strings.TrimSpace(j.Model)
	j.Thinking = strings.TrimSpace(j.Thinking)
	j.OutputSchema = strings.TrimSpace(j.OutputSchema)
	j.WorkspaceRoot = strings.TrimSpace(j.WorkspaceRoot)
	j.ToolAllowlist = CompactToolAllowlist(j.ToolAllowlist)
	return j
}

// ContractPrompt wraps a task with a machine-readable delegated-run contract.
func (j Job) ContractPrompt(task string) string {
	j = j.WithDefaults()
	task = strings.TrimSpace(task)

	lines := []string{
		"You are executing a delegated run.",
		"",
		"TASK:",
		task,
		"",
		"EXECUTION CONTRACT:",
		fmt.Sprintf("- max_tool_calls: %d", j.MaxToolCalls),
		fmt.Sprintf("- max_duration: %s", j.MaxDuration),
		fmt.Sprintf("- model_override: %s", valueOrInherit(j.Model)),
		fmt.Sprintf("- thinking_level: %s", valueOrInherit(j.Thinking)),
		fmt.Sprintf("- output_format: %s", j.OutputFormat),
		fmt.Sprintf("- memory_policy: %s", j.MemoryPolicy),
	}
	if j.OutputSchema != "" {
		lines = append(lines, fmt.Sprintf("- output_schema: %s", j.OutputSchema))
	}
	if len(j.ToolAllowlist) > 0 {
		lines = append(lines, fmt.Sprintf("- tool_allowlist: %s", strings.Join(j.ToolAllowlist, ", ")))
	}
	if j.WorkspaceRoot != "" {
		lines = append(lines, fmt.Sprintf("- workspace_root: %s", j.WorkspaceRoot))
	}

	lines = append(lines, "", "REQUIREMENTS:")
	switch j.OutputFormat {
	case OutputFormatJSON:
		lines = append(lines, "- Final response must be valid JSON.")
	case OutputFormatText:
		lines = append(lines, "- Final response must be concise plain text.")
	default:
		lines = append(lines, "- Final response must be concise Markdown.")
	}
	if j.OutputSchema != "" {
		lines = append(lines, "- Final response must follow this exact output shape: "+j.OutputSchema)
	}
	if len(j.ToolAllowlist) > 0 {
		lines = append(lines, "- Use only tools from the allowlist above.")
	}
	if j.MemoryPolicy == MemoryPolicyReadOnly {
		lines = append(lines, "- Do not write to MEMORY.md, memory/*.md, or other long-term notes.")
	}
	if j.MemoryPolicy == MemoryPolicyAllowWrites {
		lines = append(lines, "- Memory writes are allowed only when they are necessary for task completion.")
	}
	if j.WorkspaceRoot != "" {
		lines = append(lines, "- Keep file work scoped to the declared workspace_root.")
	}

	return strings.Join(lines, "\n")
}

// CompletionSummary renders a readable operator-facing summary for a delegated run.
func (j Job) CompletionSummary(result string) string {
	j = j.WithDefaults()
	result = strings.TrimSpace(result)
	if result == "" {
		result = "Task completed with no output."
	}
	result = trimForSummary(result, 1500)

	lines := []string{
		"Run contract:",
		fmt.Sprintf("- Budget: %d tool calls / %s", j.MaxToolCalls, j.MaxDuration),
		fmt.Sprintf("- Output: %s", outputSummary(j.OutputFormat, j.OutputSchema)),
		fmt.Sprintf("- Memory: %s", j.MemoryPolicy),
	}
	if j.Model != "" || j.Thinking != "" {
		lines = append(lines, fmt.Sprintf("- Model: %s", modelSummary(j.Model, j.Thinking)))
	}
	lines = append(lines, "", "Result:", result)
	return strings.Join(lines, "\n")
}

func ParseOutputFormat(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case OutputFormatText:
		return OutputFormatText, true
	case OutputFormatMarkdown:
		return OutputFormatMarkdown, true
	case OutputFormatJSON:
		return OutputFormatJSON, true
	default:
		return "", false
	}
}

func NormalizeOutputFormat(s string) string {
	if v, ok := ParseOutputFormat(s); ok {
		return v
	}
	return OutputFormatMarkdown
}

func ParseMemoryPolicy(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case MemoryPolicyInherit:
		return MemoryPolicyInherit, true
	case MemoryPolicyReadOnly:
		return MemoryPolicyReadOnly, true
	case MemoryPolicyAllowWrites:
		return MemoryPolicyAllowWrites, true
	default:
		return "", false
	}
}

func NormalizeMemoryPolicy(s string) string {
	if v, ok := ParseMemoryPolicy(s); ok {
		return v
	}
	return MemoryPolicyReadOnly
}

// CompactToolAllowlist returns a trimmed, de-duplicated allowlist.
func CompactToolAllowlist(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func outputSummary(format, schema string) string {
	if schema == "" {
		return format
	}
	return format + " (" + schema + ")"
}

func modelSummary(model, thinking string) string {
	if model == "" {
		model = "inherit"
	}
	if thinking == "" {
		thinking = "inherit"
	}
	return fmt.Sprintf("%s / thinking=%s", model, thinking)
}

func valueOrInherit(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "inherit"
	}
	return s
}

func trimForSummary(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}
