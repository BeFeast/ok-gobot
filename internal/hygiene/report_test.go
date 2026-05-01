package hygiene

import (
	"testing"
	"time"
)

func TestAnalyzeDetectsStaleOpenPR(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	report := Analyze(Snapshot{PullRequests: []PullRequest{{
		Number:      42,
		IssueNumber: 366,
		Title:       "stale report",
		State:       "open",
		Branch:      "feat/hygiene",
		SessionKey:  "agent:maestro:main",
		UpdatedAt:   now.Add(-8 * 24 * time.Hour),
	}}}, Options{Now: now})

	finding := requireFinding(t, report.SafeActions, FindingStaleOpenPR)
	if finding.ActionGroup != ActionSafe {
		t.Fatalf("ActionGroup = %q, want %q", finding.ActionGroup, ActionSafe)
	}
	if finding.Evidence.PRNumber != 42 || finding.Evidence.IssueNumber != 366 || finding.Evidence.Branch != "feat/hygiene" {
		t.Fatalf("unexpected evidence: %+v", finding.Evidence)
	}
}

func TestAnalyzeDetectsDeadWorkerFromRunningJob(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	report := Analyze(Snapshot{Jobs: []Job{{
		JobID:      "job-1",
		SessionKey: "agent:worker:main",
		Status:     "running",
		Branch:     "work/issue-366",
		StartedAt:  now.Add(-45 * time.Minute),
	}}}, Options{Now: now})

	finding := requireFinding(t, report.SafeActions, FindingDeadWorker)
	if finding.Evidence.JobID != "job-1" || finding.Evidence.SessionKey != "agent:worker:main" {
		t.Fatalf("unexpected evidence: %+v", finding.Evidence)
	}
}

func TestAnalyzeDetectsLocalMainBehindOrigin(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	report := Analyze(Snapshot{Checkout: Checkout{
		Branch:    "main",
		Upstream:  "origin/main",
		BehindBy:  3,
		CheckedAt: now.Add(-time.Minute),
	}}, Options{Now: now})

	finding := requireFinding(t, report.SafeActions, FindingCheckoutBehind)
	if finding.Evidence.Branch != "main" {
		t.Fatalf("Branch = %q, want main", finding.Evidence.Branch)
	}
}

func TestAnalyzeDetectsStaleApproval(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	report := Analyze(Snapshot{Approvals: []Approval{{
		ID:           "approval-1",
		SessionKey:   "agent:maestro:main",
		Command:      "rm -rf worktree",
		IssueNumber:  366,
		PRNumber:     42,
		Branch:       "feat/hygiene",
		WorktreePath: "/tmp/wt",
		CreatedAt:    now.Add(-20 * time.Minute),
	}}}, Options{Now: now})

	finding := requireFinding(t, report.ApprovalRequired, FindingStaleApproval)
	if finding.ActionGroup != ActionApprovalRequired {
		t.Fatalf("ActionGroup = %q, want %q", finding.ActionGroup, ActionApprovalRequired)
	}
	if finding.Evidence.PRNumber != 42 || finding.Evidence.WorktreePath != "/tmp/wt" {
		t.Fatalf("unexpected evidence: %+v", finding.Evidence)
	}
}

func TestAnalyzeDetectsOrphanedWorktree(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	report := Analyze(Snapshot{Worktrees: []Worktree{{
		ID:        "wt-1",
		Branch:    "work/closed",
		Path:      "/tmp/wt-1",
		Status:    "active",
		PRNumber:  42,
		PRState:   "closed",
		CreatedAt: now.Add(-2 * 24 * time.Hour),
	}}}, Options{Now: now})

	finding := requireFinding(t, report.ApprovalRequired, FindingOrphanedWorktree)
	if finding.Evidence.PRNumber != 42 || finding.Evidence.WorktreePath != "/tmp/wt-1" {
		t.Fatalf("unexpected evidence: %+v", finding.Evidence)
	}
}

func TestAnalyzeDetectsEligibleQueueExhaustion(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	report := Analyze(Snapshot{Queue: Queue{
		EligibleIssues: 0,
		SkippedIssues:  2,
		Reasons:        []string{"missing ready label \"ready\""},
		CheckedAt:      now.Add(-time.Minute),
	}}, Options{Now: now})

	finding := requireFinding(t, report.SafeActions, FindingEligibleQueueBlocked)
	if finding.Evidence.StateTimestamp.IsZero() {
		t.Fatalf("missing queue evidence timestamp: %+v", finding.Evidence)
	}
}

func TestAnalyzeNoProblemCaseIsClean(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	report := Analyze(Snapshot{
		PullRequests: []PullRequest{{Number: 42, State: "open", UpdatedAt: now.Add(-time.Hour)}},
		Jobs:         []Job{{JobID: "job-1", SessionKey: "agent:worker:main", Status: "running", StartedAt: now.Add(-time.Minute)}},
		Workers:      []Worker{{JobID: "job-1", SessionKey: "agent:worker:main", Running: true, Alive: true, LastLogAt: now.Add(-time.Minute)}},
		Checkout:     Checkout{Branch: "main", Upstream: "origin/main", BehindBy: 0, CheckedAt: now},
		Approvals:    []Approval{{ID: "approval-1", CreatedAt: now.Add(-time.Minute)}},
		Worktrees:    []Worktree{{ID: "wt-1", Branch: "work/open", Path: "/tmp/wt-1", Status: "active", PRNumber: 42, PRState: "open", CreatedAt: now.Add(-time.Hour)}},
		Queue:        Queue{EligibleIssues: 1, SkippedIssues: 2, CheckedAt: now},
	}, Options{Now: now})

	if report.Summary.Status != "clean" || report.Summary.TotalFindings != 0 {
		t.Fatalf("summary = %+v, want clean with no findings", report.Summary)
	}
	if len(report.SafeActions) != 0 || len(report.ApprovalRequired) != 0 {
		t.Fatalf("unexpected findings: safe=%+v approval=%+v", report.SafeActions, report.ApprovalRequired)
	}
}

func requireFinding(t *testing.T, findings []Finding, id string) Finding {
	t.Helper()
	for _, finding := range findings {
		if finding.ID == id {
			return finding
		}
	}
	t.Fatalf("finding %q not found in %+v", id, findings)
	return Finding{}
}
