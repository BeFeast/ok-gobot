package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/hygiene"
	"ok-gobot/internal/maestro"
)

func TestRenderMaestroDecisionExplainsNoWorker(t *testing.T) {
	t.Parallel()

	decision := maestro.Decision{
		Policy: maestro.Policy{ReadyLabel: "ready"},
		Skipped: []maestro.CandidateDecision{{
			Issue:       maestro.Issue{Number: 7, Title: "blocked task"},
			SkipReasons: []string{`hard-exclude label "blocked"`},
		}},
	}
	var out bytes.Buffer
	renderMaestroDecision(&out, decision, "status")

	got := out.String()
	for _, want := range []string{
		"No worker running: no eligible issue after strict intake policy.",
		"Skipped candidates:",
		`#7 blocked task - hard-exclude label "blocked"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %q", want, got)
		}
	}
}

func TestRenderMaestroDecisionShowsOverride(t *testing.T) {
	t.Parallel()

	decision := maestro.Decision{
		Policy: maestro.Policy{ReadyLabel: "ready", Override: true, OverrideReason: "ops reviewed"},
		Next: &maestro.CandidateDecision{
			Issue:           maestro.Issue{Number: 8, Title: "force task"},
			OverrideUsed:    true,
			OverrideReasons: []string{`missing ready label "ready"`},
		},
	}
	var out bytes.Buffer
	renderMaestroDecision(&out, decision, "dry-run")

	got := out.String()
	for _, want := range []string{
		"Override: ENABLED (maintainer override: ops reviewed)",
		"Selected by maintainer override.",
		`Override bypassed: missing ready label "ready"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %q", want, got)
		}
	}
}

func TestRenderMaestroHygieneReportGroupsActions(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	report := hygiene.Report{
		GeneratedAt: now,
		Summary: hygiene.Summary{
			Status:                "attention_needed",
			TotalFindings:         2,
			SafeActionCount:       1,
			ApprovalRequiredCount: 1,
		},
		SafeActions: []hygiene.Finding{{
			ID:          hygiene.FindingDeadWorker,
			Severity:    hygiene.SeverityCritical,
			Title:       "Worker stalled",
			ActionGroup: hygiene.ActionSafe,
			Evidence:    hygiene.Evidence{SessionKey: "agent:worker:main", JobID: "job-1", Branch: "work/366", StateTimestamp: now.Add(-time.Hour)},
		}},
		ApprovalRequired: []hygiene.Finding{{
			ID:          hygiene.FindingOrphanedWorktree,
			Severity:    hygiene.SeverityWarning,
			Title:       "Worktree orphaned",
			ActionGroup: hygiene.ActionApprovalRequired,
			Evidence:    hygiene.Evidence{PRNumber: 42, WorktreePath: "/tmp/wt", StateTimestamp: now.Add(-24 * time.Hour)},
		}},
	}
	var out bytes.Buffer
	renderMaestroHygieneReport(&out, report)

	got := out.String()
	for _, want := range []string{
		"Read-only: no GitHub, Maestro, or worktree cleanup actions were performed.",
		"Safe next actions:",
		"dead_worker",
		"Approval-required actions:",
		"orphaned_worktree",
		"session=agent:worker:main",
		"pr=#42",
		"worktree=/tmp/wt",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q: %q", want, got)
		}
	}
}
