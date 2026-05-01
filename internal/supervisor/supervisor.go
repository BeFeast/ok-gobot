package supervisor

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultStaleWorkerAfter = 15 * time.Minute

type StuckState string

const (
	StateClear                StuckState = "clear"
	StateWorkerNoLogProgress  StuckState = "worker_no_log_progress"
	StateDeadWorkerNoPR       StuckState = "dead_worker_no_pr"
	StatePRChecksFailing      StuckState = "pr_checks_failing"
	StatePRReviewFeedback     StuckState = "pr_review_feedback"
	StatePRMergeBlocked       StuckState = "pr_merge_blocked"
	StateRetryExhaustedOpenPR StuckState = "retry_exhausted_open_pr"
	StateWaitingMergeApproval StuckState = "waiting_merge_approval"
)

type ActionType string

const (
	ActionRefreshPRMetadata  ActionType = "refresh_pr_metadata"
	ActionCreateMissingPR    ActionType = "create_missing_pr"
	ActionCommentBlocker     ActionType = "comment_blocker"
	ActionRetryWorker        ActionType = "retry_worker"
	ActionLabelIssueBlocked  ActionType = "label_issue_blocked"
	ActionLabelIssueReady    ActionType = "label_issue_ready"
	ActionUpdateMissionBlock ActionType = "update_mission_reason"
)

type ApprovalAction string

const (
	ApprovalNone         ApprovalAction = ""
	ApprovalMergePR      ApprovalAction = "merge_pr"
	ApprovalCloseIssue   ApprovalAction = "close_issue"
	ApprovalDeleteTree   ApprovalAction = "delete_worktree"
	ApprovalChangeConfig ApprovalAction = "change_global_config"
)

type CheckState string

const (
	CheckUnknown CheckState = "unknown"
	CheckPending CheckState = "pending"
	CheckFailing CheckState = "failing"
	CheckGreen   CheckState = "green"
)

type ReviewState string

const (
	ReviewNone       ReviewState = "none"
	ReviewActionable ReviewState = "actionable"
	ReviewApproved   ReviewState = "approved"
)

type MergeState string

const (
	MergeUnknown MergeState = "unknown"
	MergeClean   MergeState = "clean"
	MergeBehind  MergeState = "behind"
	MergeDirty   MergeState = "dirty"
)

type Worker struct {
	ID             string    `json:"id"`
	JobID          string    `json:"job_id,omitempty"`
	Running        bool      `json:"running"`
	Branch         string    `json:"branch,omitempty"`
	PRNumber       int       `json:"pr_number,omitempty"`
	BranchPushed   bool      `json:"branch_pushed,omitempty"`
	StartedAt      time.Time `json:"started_at,omitempty"`
	LastProgressAt time.Time `json:"last_progress_at,omitempty"`
	Attempt        int       `json:"attempt,omitempty"`
	MaxAttempts    int       `json:"max_attempts,omitempty"`
}

type PullRequest struct {
	Number         int         `json:"number"`
	Branch         string      `json:"branch,omitempty"`
	URL            string      `json:"url,omitempty"`
	Open           bool        `json:"open"`
	Checks         CheckState  `json:"checks,omitempty"`
	Review         ReviewState `json:"review,omitempty"`
	Merge          MergeState  `json:"merge,omitempty"`
	IssueNumber    int         `json:"issue_number,omitempty"`
	RetryExhausted bool        `json:"retry_exhausted,omitempty"`
	Attempt        int         `json:"attempt,omitempty"`
	MaxAttempts    int         `json:"max_attempts,omitempty"`
}

type Snapshot struct {
	Now          time.Time     `json:"now"`
	Workers      []Worker      `json:"workers,omitempty"`
	PullRequests []PullRequest `json:"pull_requests,omitempty"`
}

type Action struct {
	Type       ActionType        `json:"type"`
	TargetKind string            `json:"target_kind"`
	TargetID   string            `json:"target_id"`
	Reason     string            `json:"reason"`
	Message    string            `json:"message,omitempty"`
	Label      string            `json:"label,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type Decision struct {
	State          StuckState     `json:"state"`
	TargetKind     string         `json:"target_kind,omitempty"`
	TargetID       string         `json:"target_id,omitempty"`
	Reason         string         `json:"reason"`
	Blocker        string         `json:"blocker,omitempty"`
	SafeActions    []Action       `json:"safe_actions,omitempty"`
	ApprovalAction ApprovalAction `json:"approval_action,omitempty"`
}

func (d Decision) TargetKey() string {
	if d.TargetKind == "" && d.TargetID == "" {
		return string(d.State)
	}
	return d.TargetKind + ":" + d.TargetID
}

func (d Decision) TransitionKey() string {
	return d.TargetKey() + ":" + string(d.State)
}

type ActionRecord struct {
	DecisionState StuckState `json:"decision_state"`
	Action        Action     `json:"action"`
	AppliedAt     string     `json:"applied_at"`
	Result        string     `json:"result,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type Status struct {
	CurrentDecision *Decision         `json:"current_decision,omitempty"`
	LastSafeAction  *ActionRecord     `json:"last_safe_action,omitempty"`
	UpdatedAt       string            `json:"updated_at,omitempty"`
	TransitionKeys  map[string]string `json:"transition_keys,omitempty"`
	AppliedActions  map[string]string `json:"applied_actions,omitempty"`
}

type Source interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

type StateStore interface {
	GetSupervisorStatus() (Status, error)
	SetSupervisorStatus(Status) error
}

type ActionResult struct {
	Message string
}

type Executor interface {
	ExecuteSafeAction(ctx context.Context, action Action) (ActionResult, error)
}

type Notifier interface {
	Notify(ctx context.Context, decision Decision) error
}

type Option func(*Supervisor)

type Supervisor struct {
	source     Source
	store      StateStore
	executor   Executor
	notifier   Notifier
	staleAfter time.Duration
	now        func() time.Time
}

func New(source Source, opts ...Option) *Supervisor {
	s := &Supervisor{
		source:     source,
		store:      NewMemoryStateStore(),
		executor:   NoopExecutor{},
		notifier:   NoopNotifier{},
		staleAfter: DefaultStaleWorkerAfter,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(s)
	}
	if s.staleAfter <= 0 {
		s.staleAfter = DefaultStaleWorkerAfter
	}
	if s.store == nil {
		s.store = NewMemoryStateStore()
	}
	if s.executor == nil {
		s.executor = NoopExecutor{}
	}
	if s.notifier == nil {
		s.notifier = NoopNotifier{}
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s
}

func WithStateStore(store StateStore) Option {
	return func(s *Supervisor) { s.store = store }
}

func WithExecutor(executor Executor) Option {
	return func(s *Supervisor) { s.executor = executor }
}

func WithNotifier(notifier Notifier) Option {
	return func(s *Supervisor) { s.notifier = notifier }
}

func WithStaleWorkerAfter(d time.Duration) Option {
	return func(s *Supervisor) { s.staleAfter = d }
}

func WithClock(now func() time.Time) Option {
	return func(s *Supervisor) { s.now = now }
}

func (s *Supervisor) RunOnce(ctx context.Context) ([]Decision, error) {
	if s.source == nil {
		return nil, fmt.Errorf("supervisor source is required")
	}
	snapshot, err := s.source.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	if snapshot.Now.IsZero() {
		snapshot.Now = s.now()
	}

	decisions := Decide(snapshot, s.staleAfter)
	status, err := s.store.GetSupervisorStatus()
	if err != nil {
		return decisions, err
	}
	status.ensureMaps()
	status.UpdatedAt = snapshot.Now.UTC().Format(time.RFC3339)
	if len(decisions) == 0 {
		clear := Decision{State: StateClear, Reason: "no stuck states detected"}
		status.CurrentDecision = &clear
		status.TransitionKeys = map[string]string{}
		status.AppliedActions = map[string]string{}
		return decisions, s.store.SetSupervisorStatus(status)
	}

	current := decisions[0]
	status.CurrentDecision = &current
	active := make(map[string]struct{}, len(decisions))
	var firstErr error
	for _, decision := range decisions {
		targetKey := decision.TargetKey()
		transitionKey := decision.TransitionKey()
		active[targetKey] = struct{}{}
		if status.TransitionKeys[targetKey] != transitionKey {
			if err := s.notifier.Notify(ctx, decision); err != nil && firstErr == nil {
				firstErr = err
			} else if err == nil {
				status.TransitionKeys[targetKey] = transitionKey
			}
		}
		for _, action := range decision.SafeActions {
			key := actionKey(decision, action)
			if _, ok := status.AppliedActions[key]; ok {
				continue
			}
			record := ActionRecord{
				DecisionState: decision.State,
				Action:        action,
				AppliedAt:     snapshot.Now.UTC().Format(time.RFC3339),
			}
			result, err := s.executor.ExecuteSafeAction(ctx, action)
			if result.Message != "" {
				record.Result = result.Message
			}
			if err != nil {
				record.Error = err.Error()
				status.LastSafeAction = &record
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			status.LastSafeAction = &record
			status.AppliedActions[key] = record.AppliedAt
		}
	}
	for targetKey := range status.TransitionKeys {
		if _, ok := active[targetKey]; !ok {
			delete(status.TransitionKeys, targetKey)
			deleteAppliedActionsForTarget(status.AppliedActions, targetKey)
		}
	}
	if err := s.store.SetSupervisorStatus(status); err != nil && firstErr == nil {
		firstErr = err
	}
	return decisions, firstErr
}

func (s *Supervisor) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Minute
	}
	if _, err := s.RunOnce(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.RunOnce(ctx); err != nil {
				return err
			}
		}
	}
}

func Decide(snapshot Snapshot, staleAfter time.Duration) []Decision {
	if staleAfter <= 0 {
		staleAfter = DefaultStaleWorkerAfter
	}
	now := snapshot.Now
	if now.IsZero() {
		now = time.Now()
	}

	decisions := make([]Decision, 0)
	for _, worker := range snapshot.Workers {
		if decision, ok := decideWorker(worker, now, staleAfter); ok {
			decisions = append(decisions, decision)
		}
	}
	for _, pr := range snapshot.PullRequests {
		if decision, ok := decidePullRequest(pr); ok {
			decisions = append(decisions, decision)
		}
	}
	return decisions
}

func decideWorker(worker Worker, now time.Time, staleAfter time.Duration) (Decision, bool) {
	workerID := workerTargetID(worker)
	if worker.Running {
		lastProgress := worker.LastProgressAt
		if lastProgress.IsZero() {
			lastProgress = worker.StartedAt
		}
		if !lastProgress.IsZero() && now.Sub(lastProgress) >= staleAfter {
			reason := fmt.Sprintf("worker %s has not produced log progress for %s", workerID, now.Sub(lastProgress).Round(time.Second))
			actions := []Action{}
			if retryPermitted(worker.Attempt, worker.MaxAttempts) {
				actions = append(actions, Action{
					Type:       ActionRetryWorker,
					TargetKind: "worker",
					TargetID:   workerID,
					Reason:     "stale worker is within retry budget",
					Metadata: map[string]string{
						"job_id":       worker.JobID,
						"attempt":      strconv.Itoa(worker.Attempt),
						"max_attempts": strconv.Itoa(worker.MaxAttempts),
					},
				})
			} else {
				actions = append(actions, Action{
					Type:       ActionCommentBlocker,
					TargetKind: "worker",
					TargetID:   workerID,
					Reason:     "stale worker retry budget is exhausted",
					Message:    reason,
				})
			}
			actions = append(actions, missionAction(workerID, reason))
			return Decision{
				State:       StateWorkerNoLogProgress,
				TargetKind:  "worker",
				TargetID:    workerID,
				Reason:      reason,
				Blocker:     "no recent worker log progress",
				SafeActions: actions,
			}, true
		}
		return Decision{}, false
	}

	if strings.TrimSpace(worker.Branch) != "" && worker.PRNumber == 0 && worker.BranchPushed {
		reason := fmt.Sprintf("worker %s is no longer running; branch %q is pushed but has no PR", workerID, worker.Branch)
		return Decision{
			State:      StateDeadWorkerNoPR,
			TargetKind: "worker",
			TargetID:   workerID,
			Reason:     reason,
			Blocker:    "branch has no pull request",
			SafeActions: []Action{
				{
					Type:       ActionCreateMissingPR,
					TargetKind: "branch",
					TargetID:   worker.Branch,
					Reason:     "dead worker left a pushed branch without a PR",
					Metadata: map[string]string{
						"worker_id": workerID,
						"job_id":    worker.JobID,
					},
				},
				missionAction(workerID, reason),
			},
		}, true
	}
	return Decision{}, false
}

func decidePullRequest(pr PullRequest) (Decision, bool) {
	if !pr.Open {
		return Decision{}, false
	}
	if pr.RetryExhausted || retryExhausted(pr.Attempt, pr.MaxAttempts) {
		reason := fmt.Sprintf("PR %s is still open after retry budget was exhausted", prTargetID(pr))
		return blockedPRDecision(StateRetryExhaustedOpenPR, pr, reason, "retry budget exhausted", ApprovalCloseIssue), true
	}
	if pr.Checks == CheckFailing {
		reason := fmt.Sprintf("PR %s has failing CI checks", prTargetID(pr))
		return blockedPRDecision(StatePRChecksFailing, pr, reason, "CI checks are failing", ApprovalNone), true
	}
	if pr.Review == ReviewActionable {
		reason := fmt.Sprintf("PR %s has actionable review feedback", prTargetID(pr))
		return blockedPRDecision(StatePRReviewFeedback, pr, reason, "review feedback requires changes", ApprovalNone), true
	}
	if pr.Merge == MergeBehind || pr.Merge == MergeDirty {
		blocker := "branch is behind base"
		if pr.Merge == MergeDirty {
			blocker = "merge state is dirty"
		}
		reason := fmt.Sprintf("PR %s is not merge-ready: %s", prTargetID(pr), blocker)
		return blockedPRDecision(StatePRMergeBlocked, pr, reason, blocker, ApprovalNone), true
	}
	if pr.Checks == CheckGreen && (pr.Merge == "" || pr.Merge == MergeClean) && pr.Review != ReviewActionable {
		reason := fmt.Sprintf("PR %s is green and waiting for explicit merge approval", prTargetID(pr))
		return Decision{
			State:          StateWaitingMergeApproval,
			TargetKind:     "pr",
			TargetID:       prTargetID(pr),
			Reason:         reason,
			Blocker:        "merge approval required",
			ApprovalAction: ApprovalMergePR,
			SafeActions: []Action{
				refreshPRAction(pr, reason),
				labelIssueAction(pr, ActionLabelIssueReady, "ready", "PR is green and waiting for merge approval"),
				missionAction(prTargetID(pr), reason),
			},
		}, true
	}
	return Decision{}, false
}

func blockedPRDecision(state StuckState, pr PullRequest, reason, blocker string, approval ApprovalAction) Decision {
	return Decision{
		State:          state,
		TargetKind:     "pr",
		TargetID:       prTargetID(pr),
		Reason:         reason,
		Blocker:        blocker,
		ApprovalAction: approval,
		SafeActions: []Action{
			refreshPRAction(pr, reason),
			{
				Type:       ActionCommentBlocker,
				TargetKind: "pr",
				TargetID:   prTargetID(pr),
				Reason:     blocker,
				Message:    reason,
			},
			labelIssueAction(pr, ActionLabelIssueBlocked, "blocked", blocker),
			missionAction(prTargetID(pr), reason),
		},
	}
}

func retryPermitted(attempt, maxAttempts int) bool {
	if maxAttempts <= 0 {
		return true
	}
	if attempt <= 0 {
		attempt = 1
	}
	return attempt < maxAttempts
}

func retryExhausted(attempt, maxAttempts int) bool {
	return maxAttempts > 0 && attempt >= maxAttempts
}

func workerTargetID(worker Worker) string {
	if strings.TrimSpace(worker.ID) != "" {
		return strings.TrimSpace(worker.ID)
	}
	if strings.TrimSpace(worker.JobID) != "" {
		return strings.TrimSpace(worker.JobID)
	}
	if strings.TrimSpace(worker.Branch) != "" {
		return strings.TrimSpace(worker.Branch)
	}
	return "unknown"
}

func prTargetID(pr PullRequest) string {
	if pr.Number > 0 {
		return strconv.Itoa(pr.Number)
	}
	if strings.TrimSpace(pr.Branch) != "" {
		return strings.TrimSpace(pr.Branch)
	}
	return "unknown"
}

func refreshPRAction(pr PullRequest, reason string) Action {
	return Action{
		Type:       ActionRefreshPRMetadata,
		TargetKind: "pr",
		TargetID:   prTargetID(pr),
		Reason:     reason,
	}
}

func labelIssueAction(pr PullRequest, actionType ActionType, label, reason string) Action {
	targetID := prTargetID(pr)
	if pr.IssueNumber > 0 {
		targetID = strconv.Itoa(pr.IssueNumber)
	}
	return Action{
		Type:       actionType,
		TargetKind: "issue",
		TargetID:   targetID,
		Label:      label,
		Reason:     reason,
	}
}

func missionAction(targetID, reason string) Action {
	return Action{
		Type:       ActionUpdateMissionBlock,
		TargetKind: "mission",
		TargetID:   targetID,
		Reason:     reason,
	}
}

func actionKey(decision Decision, action Action) string {
	parts := []string{
		decision.TransitionKey(),
		string(action.Type),
		action.TargetKind,
		action.TargetID,
		action.Label,
	}
	return strings.Join(parts, ":")
}

func deleteAppliedActionsForTarget(actions map[string]string, targetKey string) {
	for key := range actions {
		if strings.HasPrefix(key, targetKey+":") {
			delete(actions, key)
		}
	}
}

func (s *Status) ensureMaps() {
	if s.TransitionKeys == nil {
		s.TransitionKeys = make(map[string]string)
	}
	if s.AppliedActions == nil {
		s.AppliedActions = make(map[string]string)
	}
}

type MemoryStateStore struct {
	mu     sync.Mutex
	status Status
}

func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{}
}

func (s *MemoryStateStore) GetSupervisorStatus() (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStatus(s.status), nil
}

func (s *MemoryStateStore) SetSupervisorStatus(status Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = cloneStatus(status)
	return nil
}

type NoopExecutor struct{}

func (NoopExecutor) ExecuteSafeAction(_ context.Context, action Action) (ActionResult, error) {
	return ActionResult{Message: string(action.Type) + " recorded"}, nil
}

type NoopNotifier struct{}

func (NoopNotifier) Notify(context.Context, Decision) error { return nil }

func cloneStatus(status Status) Status {
	out := status
	if status.CurrentDecision != nil {
		decision := *status.CurrentDecision
		decision.SafeActions = append([]Action(nil), status.CurrentDecision.SafeActions...)
		out.CurrentDecision = &decision
	}
	if status.LastSafeAction != nil {
		record := *status.LastSafeAction
		out.LastSafeAction = &record
	}
	if status.TransitionKeys != nil {
		out.TransitionKeys = make(map[string]string, len(status.TransitionKeys))
		for k, v := range status.TransitionKeys {
			out.TransitionKeys[k] = v
		}
	}
	if status.AppliedActions != nil {
		out.AppliedActions = make(map[string]string, len(status.AppliedActions))
		for k, v := range status.AppliedActions {
			out.AppliedActions[k] = v
		}
	}
	return out
}
