package supervisor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// StuckState is the supervisor's normalized view of one recoverable stuck state.
type StuckState string

const (
	StateNone                 StuckState = "none"
	StateStaleWorker          StuckState = "running_worker_no_log_progress"
	StateBranchWithoutPR      StuckState = "dead_worker_branch_without_pr"
	StatePRChecksFailing      StuckState = "pr_checks_failing"
	StatePRReviewFeedback     StuckState = "pr_review_feedback"
	StatePRBranchDirty        StuckState = "pr_branch_behind_or_dirty"
	StateRetryExhaustedOpenPR StuckState = "retry_exhausted_open_pr"
	StateReadyForMerge        StuckState = "checks_green_waiting_for_merge_approval"
)

// CheckState captures the aggregate PR check status needed for recovery routing.
type CheckState string

const (
	ChecksUnknown CheckState = "unknown"
	ChecksPending CheckState = "pending"
	ChecksGreen   CheckState = "green"
	ChecksFailing CheckState = "failing"
)

// ReviewState captures whether a PR has review feedback the worker can act on.
type ReviewState string

const (
	ReviewNone       ReviewState = "none"
	ReviewApproved   ReviewState = "approved"
	ReviewActionable ReviewState = "actionable"
)

// ActionKind identifies safe, idempotent actions the supervisor may run.
type ActionKind string

const (
	ActionRefreshMetadata     ActionKind = "refresh_pr_check_metadata"
	ActionCreatePR            ActionKind = "create_missing_pr"
	ActionCommentBlocker      ActionKind = "comment_precise_blocker"
	ActionRetryWorker         ActionKind = "retry_worker"
	ActionLabelBlocked        ActionKind = "label_issue_blocked"
	ActionLabelReady          ActionKind = "label_issue_ready"
	ActionUpdateMissionReason ActionKind = "update_mission_control_reason"
)

// ApprovalKind identifies risky actions that must only be requested.
type ApprovalKind string

const (
	ApprovalMergePR ApprovalKind = "merge_pr"
)

const defaultStaleAfter = 30 * time.Minute

// Observation is a single issue/worker/PR snapshot supplied to the supervisor.
type Observation struct {
	Subject     string
	IssueNumber int
	Now         time.Time
	StaleAfter  time.Duration
	Worker      WorkerSnapshot
	PR          *PullRequestSnapshot
}

// WorkerSnapshot describes the local worker and branch state for one issue.
type WorkerSnapshot struct {
	ID           string
	Running      bool
	Alive        bool
	LastLogAt    time.Time
	Branch       string
	BranchPushed bool
	Attempt      int
	MaxAttempts  int
}

// PullRequestSnapshot describes the metadata the supervisor needs from GitHub.
type PullRequestSnapshot struct {
	Number       int
	Open         bool
	Checks       CheckState
	Review       ReviewState
	BranchBehind bool
	DirtyMerge   bool
}

// Action is a safe recovery operation selected by the supervisor.
type Action struct {
	Kind   ActionKind `json:"kind"`
	Target string     `json:"target,omitempty"`
	Reason string     `json:"reason,omitempty"`
}

// ApprovalAction is a risky operation that needs explicit operator approval.
type ApprovalAction struct {
	Kind   ApprovalKind `json:"kind"`
	Target string       `json:"target,omitempty"`
	Reason string       `json:"reason,omitempty"`
}

// Decision is the supervisor's current recovery decision for one subject.
type Decision struct {
	State           StuckState       `json:"state"`
	Subject         string           `json:"subject"`
	IssueNumber     int              `json:"issue_number,omitempty"`
	PRNumber        int              `json:"pr_number,omitempty"`
	Branch          string           `json:"branch,omitempty"`
	Reason          string           `json:"reason,omitempty"`
	SafeActions     []Action         `json:"safe_actions,omitempty"`
	ApprovalActions []ApprovalAction `json:"approval_actions,omitempty"`
	DecidedAt       time.Time        `json:"decided_at,omitempty"`
}

// ActionRecord records a safe action run by the supervisor.
type ActionRecord struct {
	Subject   string     `json:"subject"`
	State     StuckState `json:"state"`
	Action    Action     `json:"action"`
	CreatedAt time.Time  `json:"created_at"`
}

// Status is the Mission Control view of the supervisor.
type Status struct {
	CurrentDecision *Decision     `json:"current_decision,omitempty"`
	LastSafeAction  *ActionRecord `json:"last_safe_action,omitempty"`
	UpdatedAt       time.Time     `json:"updated_at,omitempty"`
}

// Clone returns a deep copy of status pointer fields.
func (status Status) Clone() Status {
	return cloneStatus(status)
}

// ReconcileResult reports what changed during one supervisor poll.
type ReconcileResult struct {
	Decisions     []Decision     `json:"decisions,omitempty"`
	Notifications []Decision     `json:"notifications,omitempty"`
	SafeActions   []ActionRecord `json:"safe_actions,omitempty"`
}

// SafeActionRunner executes safe recovery actions.
type SafeActionRunner interface {
	RunSafeAction(ctx context.Context, decision Decision, action Action) error
}

// Notifier emits state-transition notifications.
type Notifier interface {
	NotifySupervisorDecision(ctx context.Context, decision Decision) error
}

// Supervisor evaluates observations, emits transition notifications, and runs
// selected safe actions once per state transition.
type Supervisor struct {
	mu        sync.Mutex
	actions   SafeActionRunner
	notifier  Notifier
	lastState map[string]StuckState
	status    Status
}

// Option configures a Supervisor.
type Option func(*Supervisor)

// WithSafeActionRunner wires the action executor used for safe recovery work.
func WithSafeActionRunner(actions SafeActionRunner) Option {
	return func(s *Supervisor) {
		s.actions = actions
	}
}

// WithNotifier wires the notification sink used for state transitions.
func WithNotifier(notifier Notifier) Option {
	return func(s *Supervisor) {
		s.notifier = notifier
	}
}

// New creates a supervisor instance.
func New(opts ...Option) *Supervisor {
	s := &Supervisor{
		lastState: make(map[string]StuckState),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Reconcile evaluates observations and runs recovery only when a subject enters
// a new stuck state. Repeated polls of the same state are intentionally quiet.
func (s *Supervisor) Reconcile(ctx context.Context, observations []Observation) (ReconcileResult, error) {
	if s == nil {
		return ReconcileResult{}, fmt.Errorf("supervisor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var result ReconcileResult
	var errs []string

	for _, observation := range observations {
		decision := Evaluate(observation)
		if decision.State == StateNone || decision.Subject == "" {
			s.recordHealthy(decision)
			continue
		}

		transition := s.recordDecision(decision)
		result.Decisions = append(result.Decisions, decision)
		if !transition {
			continue
		}

		if s.notifier != nil {
			if err := s.notifier.NotifySupervisorDecision(ctx, decision); err != nil {
				errs = append(errs, fmt.Sprintf("notify %s: %v", decision.Subject, err))
			} else {
				result.Notifications = append(result.Notifications, decision)
			}
		}

		for _, action := range decision.SafeActions {
			if s.actions == nil {
				continue
			}
			if err := s.actions.RunSafeAction(ctx, decision, action); err != nil {
				errs = append(errs, fmt.Sprintf("%s %s: %v", action.Kind, decision.Subject, err))
				continue
			}
			record := ActionRecord{
				Subject:   decision.Subject,
				State:     decision.State,
				Action:    action,
				CreatedAt: decision.DecidedAt,
			}
			s.recordSafeAction(record)
			result.SafeActions = append(result.SafeActions, record)
		}
	}

	if len(errs) > 0 {
		return result, errors.New(strings.Join(errs, "; "))
	}
	return result, nil
}

// Status returns the current Mission Control snapshot.
func (s *Supervisor) Status() Status {
	if s == nil {
		return Status{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStatus(s.status)
}

// Evaluate converts a raw observation into a supervisor decision.
func Evaluate(observation Observation) Decision {
	now := observation.Now
	if now.IsZero() {
		now = time.Now()
	}
	staleAfter := observation.StaleAfter
	if staleAfter <= 0 {
		staleAfter = defaultStaleAfter
	}

	worker := observation.Worker
	pr := observation.PR
	subject := observationSubject(observation)
	base := func(state StuckState, reason string) Decision {
		return Decision{
			State:       state,
			Subject:     subject,
			IssueNumber: observation.IssueNumber,
			PRNumber:    prNumber(pr),
			Branch:      strings.TrimSpace(worker.Branch),
			Reason:      reason,
			SafeActions: []Action{{
				Kind:   ActionUpdateMissionReason,
				Target: subject,
				Reason: reason,
			}},
			DecidedAt: now,
		}
	}

	if worker.Running && !worker.LastLogAt.IsZero() && now.Sub(worker.LastLogAt) >= staleAfter {
		reason := fmt.Sprintf("worker %s has no log progress for %s", workerLabel(worker), now.Sub(worker.LastLogAt).Round(time.Second))
		decision := base(StateStaleWorker, reason)
		if retryBudgetPermits(worker) {
			decision.SafeActions = append(decision.SafeActions, Action{Kind: ActionRetryWorker, Target: workerLabel(worker), Reason: reason})
		} else {
			decision.SafeActions = append(decision.SafeActions,
				Action{Kind: ActionCommentBlocker, Target: subject, Reason: "retry budget exhausted for stale worker"},
				Action{Kind: ActionLabelBlocked, Target: subject, Reason: reason},
			)
		}
		return decision
	}

	if !worker.Running && !worker.Alive && strings.TrimSpace(worker.Branch) != "" && !hasOpenPR(pr) {
		reason := fmt.Sprintf("worker %s stopped with branch %s but no open PR", workerLabel(worker), worker.Branch)
		decision := base(StateBranchWithoutPR, reason)
		decision.SafeActions = append(decision.SafeActions, Action{Kind: ActionRefreshMetadata, Target: worker.Branch, Reason: reason})
		if worker.BranchPushed {
			decision.SafeActions = append(decision.SafeActions, Action{Kind: ActionCreatePR, Target: worker.Branch, Reason: reason})
		} else {
			decision.SafeActions = append(decision.SafeActions, Action{Kind: ActionCommentBlocker, Target: subject, Reason: "branch exists locally but has not been pushed"})
		}
		return decision
	}

	if hasOpenPR(pr) && retryExhausted(worker) {
		reason := fmt.Sprintf("retry budget exhausted at attempt %d/%d while PR #%d remains open", worker.Attempt, worker.MaxAttempts, pr.Number)
		decision := base(StateRetryExhaustedOpenPR, reason)
		decision.SafeActions = append(decision.SafeActions,
			Action{Kind: ActionRefreshMetadata, Target: prTarget(pr), Reason: reason},
			Action{Kind: ActionCommentBlocker, Target: prTarget(pr), Reason: reason},
			Action{Kind: ActionLabelBlocked, Target: subject, Reason: reason},
		)
		return decision
	}

	if hasOpenPR(pr) && pr.Checks == ChecksFailing {
		reason := fmt.Sprintf("PR #%d has failing checks", pr.Number)
		decision := base(StatePRChecksFailing, reason)
		decision.SafeActions = append(decision.SafeActions,
			Action{Kind: ActionRefreshMetadata, Target: prTarget(pr), Reason: reason},
			Action{Kind: ActionCommentBlocker, Target: prTarget(pr), Reason: reason},
			Action{Kind: ActionLabelBlocked, Target: subject, Reason: reason},
		)
		return decision
	}

	if hasOpenPR(pr) && pr.Review == ReviewActionable {
		reason := fmt.Sprintf("PR #%d has actionable review feedback", pr.Number)
		decision := base(StatePRReviewFeedback, reason)
		decision.SafeActions = append(decision.SafeActions,
			Action{Kind: ActionRefreshMetadata, Target: prTarget(pr), Reason: reason},
			Action{Kind: ActionCommentBlocker, Target: prTarget(pr), Reason: reason},
			Action{Kind: ActionLabelBlocked, Target: subject, Reason: reason},
		)
		return decision
	}

	if hasOpenPR(pr) && (pr.BranchBehind || pr.DirtyMerge) {
		reason := fmt.Sprintf("PR #%d branch is behind or has a dirty merge state", pr.Number)
		decision := base(StatePRBranchDirty, reason)
		decision.SafeActions = append(decision.SafeActions,
			Action{Kind: ActionRefreshMetadata, Target: prTarget(pr), Reason: reason},
			Action{Kind: ActionCommentBlocker, Target: prTarget(pr), Reason: reason},
			Action{Kind: ActionLabelBlocked, Target: subject, Reason: reason},
		)
		return decision
	}

	if hasOpenPR(pr) && pr.Checks == ChecksGreen && pr.Review != ReviewActionable && !pr.BranchBehind && !pr.DirtyMerge {
		reason := fmt.Sprintf("PR #%d checks are green and merge is waiting for approval", pr.Number)
		decision := base(StateReadyForMerge, reason)
		decision.SafeActions = append(decision.SafeActions,
			Action{Kind: ActionRefreshMetadata, Target: prTarget(pr), Reason: reason},
			Action{Kind: ActionLabelReady, Target: subject, Reason: reason},
		)
		decision.ApprovalActions = append(decision.ApprovalActions, ApprovalAction{Kind: ApprovalMergePR, Target: prTarget(pr), Reason: reason})
		return decision
	}

	return Decision{State: StateNone, Subject: subject, DecidedAt: now}
}

func (s *Supervisor) recordHealthy(decision Decision) {
	if decision.Subject == "" {
		return
	}
	s.mu.Lock()
	delete(s.lastState, decision.Subject)
	decisionCopy := cloneDecision(decision)
	s.status.CurrentDecision = &decisionCopy
	s.status.UpdatedAt = decision.DecidedAt
	s.mu.Unlock()
}

func (s *Supervisor) recordDecision(decision Decision) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	last := s.lastState[decision.Subject]
	transition := last != decision.State
	s.lastState[decision.Subject] = decision.State
	decisionCopy := cloneDecision(decision)
	s.status.CurrentDecision = &decisionCopy
	s.status.UpdatedAt = decision.DecidedAt
	return transition
}

func (s *Supervisor) recordSafeAction(record ActionRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recordCopy := record
	s.status.LastSafeAction = &recordCopy
	s.status.UpdatedAt = record.CreatedAt
}

func observationSubject(observation Observation) string {
	if subject := strings.TrimSpace(observation.Subject); subject != "" {
		return subject
	}
	if observation.IssueNumber > 0 {
		return fmt.Sprintf("issue-%d", observation.IssueNumber)
	}
	if observation.PR != nil && observation.PR.Number > 0 {
		return fmt.Sprintf("pr-%d", observation.PR.Number)
	}
	if branch := strings.TrimSpace(observation.Worker.Branch); branch != "" {
		return branch
	}
	return strings.TrimSpace(observation.Worker.ID)
}

func retryBudgetPermits(worker WorkerSnapshot) bool {
	if worker.MaxAttempts <= 0 {
		return false
	}
	return worker.Attempt < worker.MaxAttempts
}

func retryExhausted(worker WorkerSnapshot) bool {
	if worker.MaxAttempts <= 0 {
		return false
	}
	return worker.Attempt >= worker.MaxAttempts
}

func hasOpenPR(pr *PullRequestSnapshot) bool {
	return pr != nil && pr.Open && pr.Number > 0
}

func prNumber(pr *PullRequestSnapshot) int {
	if pr == nil {
		return 0
	}
	return pr.Number
}

func prTarget(pr *PullRequestSnapshot) string {
	if pr == nil || pr.Number <= 0 {
		return ""
	}
	return fmt.Sprintf("#%d", pr.Number)
}

func workerLabel(worker WorkerSnapshot) string {
	if id := strings.TrimSpace(worker.ID); id != "" {
		return id
	}
	return "unknown"
}

func cloneStatus(status Status) Status {
	out := status
	if status.CurrentDecision != nil {
		decision := cloneDecision(*status.CurrentDecision)
		out.CurrentDecision = &decision
	}
	if status.LastSafeAction != nil {
		action := *status.LastSafeAction
		out.LastSafeAction = &action
	}
	return out
}

func cloneDecision(decision Decision) Decision {
	out := decision
	if decision.SafeActions != nil {
		out.SafeActions = append([]Action(nil), decision.SafeActions...)
	}
	if decision.ApprovalActions != nil {
		out.ApprovalActions = append([]ApprovalAction(nil), decision.ApprovalActions...)
	}
	return out
}
