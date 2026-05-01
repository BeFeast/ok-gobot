package reliability

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

var orderedCategories = []FailureCategory{
	CategoryAgentFailure,
	CategoryEnvironmentFailure,
	CategoryCIFailure,
	CategoryReviewFailure,
	CategoryPolicyGatedSkip,
}

// JSON renders the report as stable, indented JSON.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Compact renders a terse operator-facing report for CLI output.
func (r Report) Compact() string {
	var sb strings.Builder
	name := valueOrDefault(strings.TrimSpace(r.Name), "reliability-benchmark")
	fmt.Fprintf(&sb, "Reliability benchmark: %s (%d scenarios)\n", name, r.Summary.Total)
	fmt.Fprintf(&sb, "PASS %d  FAIL %d  SKIP %d\n", r.Summary.Passed, r.Summary.Failed, r.Summary.Skipped)

	if categories := r.formatCategoryCounts(); categories != "" {
		fmt.Fprintf(&sb, "Categories: %s\n", categories)
	}

	for _, result := range r.Results {
		label := outcomeLabel(result.Outcome)
		category := ""
		if result.FailureCategory != "" && result.FailureCategory != CategoryNone {
			category = fmt.Sprintf(" [%s]", result.FailureCategory)
		}
		reason := result.Reason
		if strings.TrimSpace(result.DataSource) != "" {
			reason = fmt.Sprintf("%s (source: %s)", reason, result.DataSource)
		}
		fmt.Fprintf(&sb, "%s %s%s: %s\n", label, result.ID, category, reason)
	}
	return sb.String()
}

// Markdown renders a human-readable benchmark artifact suitable for PRs or run
// logs.
func (r Report) Markdown() string {
	var sb strings.Builder
	name := valueOrDefault(strings.TrimSpace(r.Name), "reliability-benchmark")
	fmt.Fprintf(&sb, "# Reliability Benchmark Report\n\n")
	fmt.Fprintf(&sb, "- Manifest: `%s`\n", escapeInline(name))
	if !r.GeneratedAt.IsZero() {
		fmt.Fprintf(&sb, "- Generated: `%s`\n", r.GeneratedAt.UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(&sb, "- Total: %d\n", r.Summary.Total)
	fmt.Fprintf(&sb, "- Passed: %d\n", r.Summary.Passed)
	fmt.Fprintf(&sb, "- Failed: %d\n", r.Summary.Failed)
	fmt.Fprintf(&sb, "- Skipped: %d\n", r.Summary.Skipped)
	if categories := r.formatCategoryCounts(); categories != "" {
		fmt.Fprintf(&sb, "- Categories: %s\n", categories)
	}

	includeEvidence := reportHasEvidenceMetadata(r.Results)
	if includeEvidence {
		sb.WriteString("\n| Result | Scenario | Category | Reason | Data Source | Evidence | Lifecycle |\n")
		sb.WriteString("|---|---|---|---|---|---|---|\n")
	} else {
		sb.WriteString("\n| Result | Scenario | Category | Reason | Lifecycle |\n")
		sb.WriteString("|---|---|---|---|---|\n")
	}
	for _, result := range r.Results {
		category := string(result.FailureCategory)
		if category == "" {
			category = string(CategoryNone)
		}
		if includeEvidence {
			fmt.Fprintf(&sb, "| %s | `%s` | `%s` | %s | %s | %s | %s |\n",
				escapeCell(outcomeLabel(result.Outcome)),
				escapeCell(result.ID),
				escapeCell(category),
				escapeCell(result.Reason),
				escapeCell(result.DataSource),
				formatEvidenceLinks(result.EvidenceLinks),
				escapeCell(formatLifecycle(result.Lifecycle)),
			)
		} else {
			fmt.Fprintf(&sb, "| %s | `%s` | `%s` | %s | %s |\n",
				escapeCell(outcomeLabel(result.Outcome)),
				escapeCell(result.ID),
				escapeCell(category),
				escapeCell(result.Reason),
				escapeCell(formatLifecycle(result.Lifecycle)),
			)
		}
	}
	return sb.String()
}

func (r Report) formatCategoryCounts() string {
	var parts []string
	for _, category := range orderedCategories {
		count := r.Summary.Categories[category]
		if count == 0 {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%d", category, count))
	}
	return strings.Join(parts, "  ")
}

func outcomeLabel(outcome Outcome) string {
	switch outcome {
	case OutcomeMergeReady:
		return "PASS"
	case OutcomeSkipped:
		return "SKIP"
	default:
		return "FAIL"
	}
}

func formatLifecycle(events []LifecycleEvent) string {
	if len(events) == 0 {
		return "not recorded"
	}
	parts := make([]string, 0, len(events))
	for _, event := range events {
		status := event.Status
		if status == "" {
			status = EventStatusPassed
		}
		parts = append(parts, fmt.Sprintf("%s:%s", event.State, status))
	}
	return strings.Join(parts, " -> ")
}

func reportHasEvidenceMetadata(results []ScenarioResult) bool {
	for _, result := range results {
		if strings.TrimSpace(result.DataSource) != "" || len(result.EvidenceLinks) > 0 {
			return true
		}
	}
	return false
}

func formatEvidenceLinks(links []EvidenceLink) string {
	if len(links) == 0 {
		return ""
	}
	parts := make([]string, 0, len(links))
	for _, link := range links {
		label := strings.TrimSpace(link.Label)
		if label == "" {
			label = strings.TrimSpace(link.Type)
		}
		url := strings.TrimSpace(link.URL)
		if label == "" || url == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s](%s)", escapeCell(label), escapeCell(url)))
	}
	return strings.Join(parts, ", ")
}

func escapeCell(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", `\|`)
	return strings.TrimSpace(value)
}

func escapeInline(value string) string {
	return strings.ReplaceAll(value, "`", "'")
}
