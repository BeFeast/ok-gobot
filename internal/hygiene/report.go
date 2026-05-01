package hygiene

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

type ActionGroup string

const (
	ActionSafe             ActionGroup = "safe"
	ActionApprovalRequired ActionGroup = "approval_required"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

const (
	FindingStaleOpenPR           = "stale_open_pr"
	FindingClosedPRSession       = "closed_pr_leftover_session"
	FindingDeadWorker            = "dead_worker"
	FindingCheckoutBehind        = "checkout_behind_origin"
	FindingStaleApproval         = "stale_approval"
	FindingOrphanedWorktree      = "orphaned_worktree"
	FindingEligibleQueueBlocked  = "eligible_queue_exhausted"
	FindingPartialCollectionData = "partial_collection_data"
)

type Evidence struct {
	SessionKey     string    `json:"session_key,omitempty"`
	JobID          string    `json:"job_id,omitempty"`
	IssueNumber    int       `json:"issue,omitempty"`
	PRNumber       int       `json:"pr,omitempty"`
	Branch         string    `json:"branch,omitempty"`
	WorktreePath   string    `json:"worktree_path,omitempty"`
	StateTimestamp time.Time `json:"state_timestamp,omitempty"`
}

type Finding struct {
	ID             string      `json:"id"`
	Severity       Severity    `json:"severity"`
	Title          string      `json:"title"`
	Detail         string      `json:"detail"`
	Recommendation string      `json:"recommendation"`
	ActionGroup    ActionGroup `json:"action_group"`
	Evidence       Evidence    `json:"evidence"`
	DetectedAt     time.Time   `json:"detected_at"`
}

type Summary struct {
	Status                string    `json:"status"`
	TotalFindings         int       `json:"total_findings"`
	SafeActionCount       int       `json:"safe_action_count"`
	ApprovalRequiredCount int       `json:"approval_required_count"`
	GeneratedAt           time.Time `json:"generated_at"`
}

type Report struct {
	GeneratedAt      time.Time `json:"generated_at"`
	Summary          Summary   `json:"summary"`
	SafeActions      []Finding `json:"safe_actions"`
	ApprovalRequired []Finding `json:"approval_required_actions"`
	Warnings         []Finding `json:"warnings,omitempty"`
}

type PullRequest struct {
	Number      int
	IssueNumber int
	Title       string
	State       string
	Branch      string
	SessionKey  string
	UpdatedAt   time.Time
}

type Session struct {
	Key         string
	IssueNumber int
	PRNumber    int
	Branch      string
	UpdatedAt   time.Time
}

type Worker struct {
	SessionKey string
	JobID      string
	Branch     string
	Running    bool
	Alive      bool
	LastSeenAt time.Time
	LastLogAt  time.Time
	QueueDepth int
}

type Job struct {
	JobID       string
	SessionKey  string
	Status      string
	Branch      string
	IssueNumber int
	PRNumber    int
	Attempt     int
	MaxAttempts int
	CreatedAt   time.Time
	StartedAt   time.Time
	UpdatedAt   time.Time
}

type Worktree struct {
	ID        string
	Branch    string
	Path      string
	RepoRoot  string
	Status    string
	PRNumber  int
	PRState   string
	CreatedAt time.Time
}

type Checkout struct {
	Branch    string
	Upstream  string
	BehindBy  int
	AheadBy   int
	CheckedAt time.Time
}

type Approval struct {
	ID           string
	SessionKey   string
	Command      string
	IssueNumber  int
	PRNumber     int
	Branch       string
	WorktreePath string
	CreatedAt    time.Time
}

type Queue struct {
	EligibleIssues int
	SkippedIssues  int
	Reasons        []string
	CheckedAt      time.Time
}

type Snapshot struct {
	GeneratedAt  time.Time
	PullRequests []PullRequest
	Sessions     []Session
	Workers      []Worker
	Jobs         []Job
	Worktrees    []Worktree
	Checkout     Checkout
	Approvals    []Approval
	Queue        Queue
	Warnings     []string
}

type Options struct {
	Now                 time.Time
	StaleOpenPRAge      time.Duration
	DeadWorkerAge       time.Duration
	StaleApprovalAge    time.Duration
	OrphanedWorktreeAge time.Duration
}

func DefaultOptions() Options {
	return Options{
		StaleOpenPRAge:      7 * 24 * time.Hour,
		DeadWorkerAge:       30 * time.Minute,
		StaleApprovalAge:    15 * time.Minute,
		OrphanedWorktreeAge: 7 * 24 * time.Hour,
	}
}

func Analyze(snapshot Snapshot, opts Options) Report {
	defaults := DefaultOptions()
	if opts.StaleOpenPRAge <= 0 {
		opts.StaleOpenPRAge = defaults.StaleOpenPRAge
	}
	if opts.DeadWorkerAge <= 0 {
		opts.DeadWorkerAge = defaults.DeadWorkerAge
	}
	if opts.StaleApprovalAge <= 0 {
		opts.StaleApprovalAge = defaults.StaleApprovalAge
	}
	if opts.OrphanedWorktreeAge <= 0 {
		opts.OrphanedWorktreeAge = defaults.OrphanedWorktreeAge
	}

	now := opts.Now
	if now.IsZero() {
		now = snapshot.GeneratedAt
	}
	if now.IsZero() {
		now = time.Now()
	}

	report := Report{GeneratedAt: now}
	closedPRs := closedPRMap(snapshot.PullRequests)
	openPRBranches := openPRBranchSet(snapshot.PullRequests)
	liveWorkers := liveWorkerKeys(snapshot.Workers)

	for _, pr := range snapshot.PullRequests {
		if !isOpenState(pr.State) || pr.UpdatedAt.IsZero() || now.Sub(pr.UpdatedAt) < opts.StaleOpenPRAge {
			continue
		}
		report.add(Finding{
			ID:             FindingStaleOpenPR,
			Severity:       SeverityWarning,
			Title:          fmt.Sprintf("Open PR #%d has gone stale", pr.Number),
			Detail:         fmt.Sprintf("PR #%d %s has not changed for %s.", pr.Number, strings.TrimSpace(pr.Title), roundAge(now.Sub(pr.UpdatedAt))),
			Recommendation: "Refresh PR metadata, check CI/review state, and restart or steer the worker before spending more queue time.",
			ActionGroup:    ActionSafe,
			Evidence: Evidence{
				SessionKey:     pr.SessionKey,
				IssueNumber:    pr.IssueNumber,
				PRNumber:       pr.Number,
				Branch:         pr.Branch,
				StateTimestamp: pr.UpdatedAt,
			},
			DetectedAt: now,
		})
	}

	for _, session := range snapshot.Sessions {
		pr, ok := closedPRs[session.PRNumber]
		if !ok || session.PRNumber <= 0 {
			continue
		}
		timestamp := session.UpdatedAt
		if timestamp.IsZero() {
			timestamp = pr.UpdatedAt
		}
		report.add(Finding{
			ID:             FindingClosedPRSession,
			Severity:       SeverityWarning,
			Title:          fmt.Sprintf("Session still references closed PR #%d", session.PRNumber),
			Detail:         fmt.Sprintf("Session %s still points at PR #%d after GitHub state became %s.", session.Key, session.PRNumber, normalizeState(pr.State)),
			Recommendation: "Archive or remove the leftover session only after the operator confirms no follow-up work is needed.",
			ActionGroup:    ActionApprovalRequired,
			Evidence: Evidence{
				SessionKey:     session.Key,
				IssueNumber:    firstPositive(session.IssueNumber, pr.IssueNumber),
				PRNumber:       session.PRNumber,
				Branch:         valueOr(session.Branch, pr.Branch),
				StateTimestamp: timestamp,
			},
			DetectedAt: now,
		})
	}

	seenDeadWorkers := map[string]struct{}{}
	for _, worker := range snapshot.Workers {
		key := workerKey(worker.SessionKey, worker.JobID)
		if worker.Alive && (!worker.Running || worker.LastLogAt.IsZero() || now.Sub(worker.LastLogAt) < opts.DeadWorkerAge) {
			continue
		}
		timestamp := worker.LastLogAt
		if timestamp.IsZero() {
			timestamp = worker.LastSeenAt
		}
		if timestamp.IsZero() || now.Sub(timestamp) < opts.DeadWorkerAge {
			continue
		}
		seenDeadWorkers[key] = struct{}{}
		report.add(deadWorkerFinding(now, worker.SessionKey, worker.JobID, worker.Branch, timestamp))
	}

	for _, job := range snapshot.Jobs {
		if normalizeState(job.Status) != "running" || job.StartedAt.IsZero() || now.Sub(job.StartedAt) < opts.DeadWorkerAge {
			continue
		}
		key := workerKey(job.SessionKey, job.JobID)
		if _, seen := liveWorkers[key]; seen {
			continue
		}
		if _, seen := liveWorkers[workerKey(job.SessionKey, "")]; seen {
			continue
		}
		if _, seen := seenDeadWorkers[key]; seen {
			continue
		}
		report.add(deadWorkerFinding(now, job.SessionKey, job.JobID, job.Branch, job.StartedAt))
	}

	if snapshot.Checkout.BehindBy > 0 {
		branch := valueOr(snapshot.Checkout.Branch, "main")
		upstream := valueOr(snapshot.Checkout.Upstream, "origin/"+branch)
		report.add(Finding{
			ID:             FindingCheckoutBehind,
			Severity:       SeverityWarning,
			Title:          fmt.Sprintf("Local %s is behind %s", branch, upstream),
			Detail:         fmt.Sprintf("%s is %d commit(s) behind %s.", branch, snapshot.Checkout.BehindBy, upstream),
			Recommendation: "Update the source checkout before dispatching more workers so new branches start from current origin state.",
			ActionGroup:    ActionSafe,
			Evidence: Evidence{
				Branch:         branch,
				StateTimestamp: snapshot.Checkout.CheckedAt,
			},
			DetectedAt: now,
		})
	}

	for _, approval := range snapshot.Approvals {
		if approval.CreatedAt.IsZero() || now.Sub(approval.CreatedAt) < opts.StaleApprovalAge {
			continue
		}
		report.add(Finding{
			ID:             FindingStaleApproval,
			Severity:       SeverityCritical,
			Title:          fmt.Sprintf("Approval %s is stale", approval.ID),
			Detail:         fmt.Sprintf("Approval %s has waited for %s.", approval.ID, roundAge(now.Sub(approval.CreatedAt))),
			Recommendation: "Review the command and explicitly approve or deny it; do not let blocked worker state accumulate.",
			ActionGroup:    ActionApprovalRequired,
			Evidence: Evidence{
				SessionKey:     approval.SessionKey,
				IssueNumber:    approval.IssueNumber,
				PRNumber:       approval.PRNumber,
				Branch:         approval.Branch,
				WorktreePath:   approval.WorktreePath,
				StateTimestamp: approval.CreatedAt,
			},
			DetectedAt: now,
		})
	}

	for _, worktree := range snapshot.Worktrees {
		if !isOrphanedWorktree(now, worktree, openPRBranches, opts.OrphanedWorktreeAge) {
			continue
		}
		state := normalizeState(valueOr(worktree.PRState, worktree.Status))
		if state == "" {
			state = "unlinked"
		}
		report.add(Finding{
			ID:             FindingOrphanedWorktree,
			Severity:       SeverityWarning,
			Title:          fmt.Sprintf("Worktree %s appears orphaned", valueOr(worktree.ID, worktree.Branch)),
			Detail:         fmt.Sprintf("Worktree %s on branch %s is %s.", valueOr(worktree.Path, worktree.ID), valueOr(worktree.Branch, "unknown"), state),
			Recommendation: "Remove the worktree/branch only after explicit operator approval and after checking for unpushed work.",
			ActionGroup:    ActionApprovalRequired,
			Evidence: Evidence{
				PRNumber:       worktree.PRNumber,
				Branch:         worktree.Branch,
				WorktreePath:   worktree.Path,
				StateTimestamp: worktree.CreatedAt,
			},
			DetectedAt: now,
		})
	}

	if !snapshot.Queue.CheckedAt.IsZero() && snapshot.Queue.EligibleIssues == 0 && snapshot.Queue.SkippedIssues > 0 {
		detail := fmt.Sprintf("Maestro found no eligible issues and skipped %d candidate(s).", snapshot.Queue.SkippedIssues)
		if len(snapshot.Queue.Reasons) > 0 {
			detail += " Top gates: " + strings.Join(snapshot.Queue.Reasons, "; ") + "."
		}
		report.add(Finding{
			ID:             FindingEligibleQueueBlocked,
			Severity:       SeverityWarning,
			Title:          "Eligible queue is exhausted by policy",
			Detail:         detail,
			Recommendation: "Unblock labels/dependencies or use a documented maintainer override before starting another worker.",
			ActionGroup:    ActionSafe,
			Evidence: Evidence{
				StateTimestamp: snapshot.Queue.CheckedAt,
			},
			DetectedAt: now,
		})
	}

	for _, warning := range snapshot.Warnings {
		warning = strings.TrimSpace(warning)
		if warning == "" {
			continue
		}
		report.Warnings = append(report.Warnings, Finding{
			ID:             FindingPartialCollectionData,
			Severity:       SeverityInfo,
			Title:          "Partial hygiene data",
			Detail:         warning,
			Recommendation: "Fix the data source and rerun the read-only hygiene report for a complete view.",
			ActionGroup:    ActionSafe,
			DetectedAt:     now,
		})
	}

	report.sort()
	report.Summary = Summary{
		Status:                reportStatus(report),
		TotalFindings:         len(report.SafeActions) + len(report.ApprovalRequired),
		SafeActionCount:       len(report.SafeActions),
		ApprovalRequiredCount: len(report.ApprovalRequired),
		GeneratedAt:           now,
	}
	return report
}

func (r *Report) add(f Finding) {
	if f.DetectedAt.IsZero() {
		f.DetectedAt = r.GeneratedAt
	}
	if f.ActionGroup == ActionApprovalRequired {
		r.ApprovalRequired = append(r.ApprovalRequired, f)
		return
	}
	f.ActionGroup = ActionSafe
	r.SafeActions = append(r.SafeActions, f)
}

func (r *Report) sort() {
	sortFindings := func(findings []Finding) {
		sort.SliceStable(findings, func(i, j int) bool {
			if findings[i].ID != findings[j].ID {
				return findings[i].ID < findings[j].ID
			}
			return evidenceKey(findings[i].Evidence) < evidenceKey(findings[j].Evidence)
		})
	}
	sortFindings(r.SafeActions)
	sortFindings(r.ApprovalRequired)
	sortFindings(r.Warnings)
}

func reportStatus(report Report) string {
	if len(report.SafeActions)+len(report.ApprovalRequired) == 0 {
		return "clean"
	}
	return "attention_needed"
}

func closedPRMap(prs []PullRequest) map[int]PullRequest {
	out := map[int]PullRequest{}
	for _, pr := range prs {
		if pr.Number > 0 && isClosedState(pr.State) {
			out[pr.Number] = pr
		}
	}
	return out
}

func openPRBranchSet(prs []PullRequest) map[string]struct{} {
	out := map[string]struct{}{}
	for _, pr := range prs {
		branch := strings.TrimSpace(pr.Branch)
		if branch != "" && isOpenState(pr.State) {
			out[branch] = struct{}{}
		}
	}
	return out
}

func liveWorkerKeys(workers []Worker) map[string]struct{} {
	out := map[string]struct{}{}
	for _, worker := range workers {
		if worker.Alive && worker.Running {
			out[workerKey(worker.SessionKey, worker.JobID)] = struct{}{}
			if strings.TrimSpace(worker.SessionKey) != "" {
				out[workerKey(worker.SessionKey, "")] = struct{}{}
			}
		}
	}
	return out
}

func deadWorkerFinding(now time.Time, sessionKey, jobID, branch string, timestamp time.Time) Finding {
	target := valueOr(sessionKey, jobID)
	if target == "" {
		target = valueOr(branch, "unknown worker")
	}
	return Finding{
		ID:             FindingDeadWorker,
		Severity:       SeverityCritical,
		Title:          fmt.Sprintf("Worker %s has no live progress", target),
		Detail:         fmt.Sprintf("Worker %s has not produced live progress for %s.", target, roundAge(now.Sub(timestamp))),
		Recommendation: "Inspect the worker log/tmux tail, then safely restart or mark the job blocked before dispatching more work.",
		ActionGroup:    ActionSafe,
		Evidence: Evidence{
			SessionKey:     sessionKey,
			JobID:          jobID,
			Branch:         branch,
			StateTimestamp: timestamp,
		},
		DetectedAt: now,
	}
}

func isOrphanedWorktree(now time.Time, worktree Worktree, openPRBranches map[string]struct{}, staleAge time.Duration) bool {
	status := normalizeState(worktree.Status)
	prState := normalizeState(worktree.PRState)
	if status == "merged" || status == "closed" || status == "stale" || prState == "merged" || prState == "closed" {
		return true
	}
	if worktree.Branch != "" {
		if _, ok := openPRBranches[worktree.Branch]; ok {
			return false
		}
	}
	return worktree.PRNumber == 0 && !worktree.CreatedAt.IsZero() && now.Sub(worktree.CreatedAt) >= staleAge
}

func workerKey(sessionKey, jobID string) string {
	return strings.TrimSpace(sessionKey) + "\x00" + strings.TrimSpace(jobID)
}

func evidenceKey(e Evidence) string {
	return fmt.Sprintf("%s/%s/%d/%d/%s/%s", e.SessionKey, e.JobID, e.IssueNumber, e.PRNumber, e.Branch, e.WorktreePath)
}

func normalizeState(state string) string {
	return strings.ToLower(strings.TrimSpace(state))
}

func isOpenState(state string) bool {
	return normalizeState(state) == "open"
}

func isClosedState(state string) bool {
	state = normalizeState(state)
	return state == "closed" || state == "merged"
}

func roundAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= 24*time.Hour {
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
	if d >= time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}
