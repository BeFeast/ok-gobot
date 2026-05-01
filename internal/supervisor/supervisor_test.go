package supervisor

import (
	"context"
	"testing"
	"time"
)

func TestDecideCoversStuckStates(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	staleAfter := 10 * time.Minute

	tests := []struct {
		name       string
		snapshot   Snapshot
		wantState  StuckState
		wantAction ActionType
		wantLabel  string
		wantAsk    ApprovalAction
	}{
		{
			name: "running worker with no log progress",
			snapshot: Snapshot{Now: now, Workers: []Worker{{
				ID:             "worker-stale",
				JobID:          "job-stale",
				Running:        true,
				StartedAt:      now.Add(-30 * time.Minute),
				LastProgressAt: now.Add(-20 * time.Minute),
				Attempt:        1,
				MaxAttempts:    2,
			}}},
			wantState:  StateWorkerNoLogProgress,
			wantAction: ActionRetryWorker,
		},
		{
			name: "dead worker with branch but no PR",
			snapshot: Snapshot{Now: now, Workers: []Worker{{
				ID:           "worker-dead",
				Branch:       "feat/dead-worker",
				BranchPushed: true,
			}}},
			wantState:  StateDeadWorkerNoPR,
			wantAction: ActionCreateMissingPR,
		},
		{
			name: "PR open with failing CI",
			snapshot: Snapshot{Now: now, PullRequests: []PullRequest{{
				Number:      101,
				Open:        true,
				Checks:      CheckFailing,
				IssueNumber: 356,
			}}},
			wantState:  StatePRChecksFailing,
			wantAction: ActionCommentBlocker,
			wantLabel:  "blocked",
		},
		{
			name: "PR open with review feedback",
			snapshot: Snapshot{Now: now, PullRequests: []PullRequest{{
				Number:      102,
				Open:        true,
				Checks:      CheckPending,
				Review:      ReviewActionable,
				IssueNumber: 356,
			}}},
			wantState:  StatePRReviewFeedback,
			wantAction: ActionCommentBlocker,
			wantLabel:  "blocked",
		},
		{
			name: "PR branch behind",
			snapshot: Snapshot{Now: now, PullRequests: []PullRequest{{
				Number:      103,
				Open:        true,
				Checks:      CheckPending,
				Merge:       MergeBehind,
				IssueNumber: 356,
			}}},
			wantState:  StatePRMergeBlocked,
			wantAction: ActionCommentBlocker,
			wantLabel:  "blocked",
		},
		{
			name: "retry exhausted with open PR",
			snapshot: Snapshot{Now: now, PullRequests: []PullRequest{{
				Number:         104,
				Open:           true,
				RetryExhausted: true,
				IssueNumber:    356,
			}}},
			wantState:  StateRetryExhaustedOpenPR,
			wantAction: ActionCommentBlocker,
			wantLabel:  "blocked",
			wantAsk:    ApprovalCloseIssue,
		},
		{
			name: "checks green waiting for merge approval",
			snapshot: Snapshot{Now: now, PullRequests: []PullRequest{{
				Number:      105,
				Open:        true,
				Checks:      CheckGreen,
				Review:      ReviewApproved,
				Merge:       MergeClean,
				IssueNumber: 356,
			}}},
			wantState:  StateWaitingMergeApproval,
			wantAction: ActionLabelIssueReady,
			wantLabel:  "ready",
			wantAsk:    ApprovalMergePR,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decisions := Decide(tt.snapshot, staleAfter)
			if len(decisions) != 1 {
				t.Fatalf("decision count = %d, want 1 (%+v)", len(decisions), decisions)
			}
			decision := decisions[0]
			if decision.State != tt.wantState {
				t.Fatalf("state = %q, want %q", decision.State, tt.wantState)
			}
			if tt.wantAsk != "" && decision.ApprovalAction != tt.wantAsk {
				t.Fatalf("approval action = %q, want %q", decision.ApprovalAction, tt.wantAsk)
			}
			if tt.wantAction != "" && !hasAction(decision.SafeActions, tt.wantAction) {
				t.Fatalf("actions %+v do not include %q", decision.SafeActions, tt.wantAction)
			}
			if tt.wantLabel != "" && !hasLabel(decision.SafeActions, tt.wantLabel) {
				t.Fatalf("actions %+v do not include label %q", decision.SafeActions, tt.wantLabel)
			}
			if !hasAction(decision.SafeActions, ActionUpdateMissionBlock) {
				t.Fatalf("actions %+v do not update Mission Control reason block", decision.SafeActions)
			}
		})
	}
}

func TestRunOnceNotifiesOncePerStateTransition(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	source := &fakeSource{snapshot: Snapshot{Now: now, Workers: []Worker{{
		ID:             "worker-stale",
		Running:        true,
		LastProgressAt: now.Add(-20 * time.Minute),
		Attempt:        1,
		MaxAttempts:    2,
	}}}}
	executor := &recordingExecutor{}
	notifier := &recordingNotifier{}
	store := NewMemoryStateStore()
	s := New(source,
		WithStateStore(store),
		WithExecutor(executor),
		WithNotifier(notifier),
		WithStaleWorkerAfter(10*time.Minute),
		WithClock(func() time.Time { return now }),
	)

	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if got := notifier.count(); got != 1 {
		t.Fatalf("notifications after repeated same state = %d, want 1", got)
	}
	if got := executor.count(); got != 2 {
		t.Fatalf("safe actions after repeated same state = %d, want 2", got)
	}

	source.snapshot = Snapshot{Now: now.Add(time.Minute), PullRequests: []PullRequest{{
		Number:      42,
		Open:        true,
		Checks:      CheckFailing,
		IssueNumber: 356,
	}}}
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("transition RunOnce: %v", err)
	}
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("repeated transitioned RunOnce: %v", err)
	}
	if got := notifier.count(); got != 2 {
		t.Fatalf("notifications after one state transition = %d, want 2", got)
	}
	if got := executor.count(); got != 6 {
		t.Fatalf("safe actions after one state transition = %d, want 6", got)
	}
}

func TestMissionStatusTracksDecisionAndLastSafeAction(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStateStore()
	s := New(&fakeSource{snapshot: Snapshot{Now: now, PullRequests: []PullRequest{{
		Number:      77,
		Open:        true,
		Checks:      CheckGreen,
		Review:      ReviewApproved,
		Merge:       MergeClean,
		IssueNumber: 356,
	}}}}, WithStateStore(store), WithExecutor(&recordingExecutor{}))

	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	status, err := store.GetSupervisorStatus()
	if err != nil {
		t.Fatalf("GetSupervisorStatus: %v", err)
	}
	if status.CurrentDecision == nil || status.CurrentDecision.State != StateWaitingMergeApproval {
		t.Fatalf("current decision = %+v, want waiting merge approval", status.CurrentDecision)
	}
	if status.LastSafeAction == nil || status.LastSafeAction.Action.Type != ActionUpdateMissionBlock {
		t.Fatalf("last safe action = %+v, want mission reason update", status.LastSafeAction)
	}
}

func hasAction(actions []Action, want ActionType) bool {
	for _, action := range actions {
		if action.Type == want {
			return true
		}
	}
	return false
}

func hasLabel(actions []Action, want string) bool {
	for _, action := range actions {
		if action.Label == want {
			return true
		}
	}
	return false
}

type fakeSource struct {
	snapshot Snapshot
}

func (f *fakeSource) Snapshot(context.Context) (Snapshot, error) {
	return f.snapshot, nil
}

type recordingExecutor struct {
	actions []Action
}

func (r *recordingExecutor) ExecuteSafeAction(_ context.Context, action Action) (ActionResult, error) {
	r.actions = append(r.actions, action)
	return ActionResult{Message: string(action.Type)}, nil
}

func (r *recordingExecutor) count() int { return len(r.actions) }

type recordingNotifier struct {
	decisions []Decision
}

func (r *recordingNotifier) Notify(_ context.Context, decision Decision) error {
	r.decisions = append(r.decisions, decision)
	return nil
}

func (r *recordingNotifier) count() int { return len(r.decisions) }
