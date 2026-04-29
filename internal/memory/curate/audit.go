package curate

import (
	"fmt"
	"regexp"
	"strings"
)

// AuditSeverity indicates how serious an audit finding is. Errors block apply;
// warnings are surfaced for reviewer attention but do not block.
type AuditSeverity string

const (
	AuditError   AuditSeverity = "error"
	AuditWarning AuditSeverity = "warning"
	AuditInfo    AuditSeverity = "info"
)

// AuditFinding is a single observation about a draft.
type AuditFinding struct {
	Severity    AuditSeverity
	CandidateID string // empty when the finding is about the draft as a whole
	Message     string
}

// String renders the finding for CLI / Telegram display.
func (f AuditFinding) String() string {
	if f.CandidateID != "" {
		return fmt.Sprintf("[%s] %s: %s", f.Severity, f.CandidateID, f.Message)
	}
	return fmt.Sprintf("[%s] %s", f.Severity, f.Message)
}

// AuditReport is the result of running the safety audit over a draft.
type AuditReport struct {
	Findings []AuditFinding
}

// HasErrors reports whether any finding is severe enough to block apply.
func (r AuditReport) HasErrors() bool {
	for _, f := range r.Findings {
		if f.Severity == AuditError {
			return true
		}
	}
	return false
}

// secretPatterns flags candidates that look like leaked secrets. Matches are
// deliberately conservative — false positives are surfaced as warnings, not
// errors, and the apply gate is the admin's explicit confirmation.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(api[_-]?key|secret|token|password|passwd|pwd)\b\s*[:=]`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),                   // AWS access key
	regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),               // GitHub personal access token
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),       // Slack token
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`), // PEM block
}

// destructivePatterns flags candidates that look like raw shell commands that
// would be unsafe to surface as durable memory.
var destructivePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+-rf\b`),
	regexp.MustCompile(`\bDROP\s+(TABLE|DATABASE)\b`),
	regexp.MustCompile(`\bdelete\s+from\b`),
	regexp.MustCompile(`>\s*/dev/`),
}

// AuditDraft runs the safety audit over a draft. It does not mutate the
// draft; the caller decides whether to apply, reject, or edit and re-audit.
func AuditDraft(d *Draft) AuditReport {
	if d == nil {
		return AuditReport{Findings: []AuditFinding{{
			Severity: AuditError,
			Message:  "draft is nil",
		}}}
	}

	var findings []AuditFinding

	if d.SourceCount == 0 {
		findings = append(findings, AuditFinding{
			Severity: AuditInfo,
			Message:  "no daily notes found in the requested range",
		})
	}

	if d.IsEmpty() {
		findings = append(findings, AuditFinding{
			Severity: AuditInfo,
			Message:  "draft has no candidates worth promoting",
		})
		// No further per-candidate checks possible.
		return AuditReport{Findings: findings}
	}

	for _, c := range d.Candidates {
		text := c.Text
		// Conflict marker → warning so reviewer must read it.
		if len(c.Conflicts) > 0 {
			findings = append(findings, AuditFinding{
				Severity:    AuditWarning,
				CandidateID: c.ID,
				Message:     fmt.Sprintf("conflicting values across notes (peers: %s)", strings.Join(c.Conflicts, ", ")),
			})
		}
		// Secret leakage → block apply.
		for _, re := range secretPatterns {
			if re.MatchString(text) {
				findings = append(findings, AuditFinding{
					Severity:    AuditError,
					CandidateID: c.ID,
					Message:     "candidate looks like it contains a credential or secret",
				})
				break
			}
		}
		// Destructive shell snippets → block apply.
		for _, re := range destructivePatterns {
			if re.MatchString(text) {
				findings = append(findings, AuditFinding{
					Severity:    AuditError,
					CandidateID: c.ID,
					Message:     "candidate contains a destructive shell snippet; do not promote raw commands",
				})
				break
			}
		}
		// Very low confidence → warn so reviewer is forced to consider it.
		if c.Confidence > 0 && c.Confidence < 0.5 {
			findings = append(findings, AuditFinding{
				Severity:    AuditInfo,
				CandidateID: c.ID,
				Message:     fmt.Sprintf("low extraction confidence (%.2f); review carefully", c.Confidence),
			})
		}
	}

	return AuditReport{Findings: findings}
}
