package supervisor

import (
	"context"
	"testing"
	"time"
)

func TestEvaluateCoversStuckStates(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		obs        Observation
		wantState  StuckState
		wantAction ActionKind
	}{
		{
			name: "running worker with no log progress retries within budget",
			obs: Observation{
				Subject:    "issue-356",
				Now:        now,
				StaleAfter: 10 * time.Minute,
				Worker: WorkerSnapshot{
					ID:          "worker-stale",
					Running:     true,
					Alive:       true,
					LastLogAt:   now.Add(-11 * time.Minute),
					Attempt:     1,
					MaxAttempts: 2,
				},
			},
			wantState:  StateStaleWorker,
			wantAction: ActionRetryWorker,
		},
		{
			name: "dead worker with pushed branch but no PR creates PR",
			obs: Observation{
				Subject: "issue-356",
				Now:     now,
				Worker: WorkerSnapshot{
					ID:           "worker-dead",
					Running:      false,
					Alive:        false,
					Branch:       "feat/issue-356",
					BranchPushed: true,
				},
			},
			wantState:  StateBranchWithoutPR,
			wantAction: ActionCreatePR,
		},
		{
			name: "open PR with failing checks comments blocker",
			obs: Observation{
				Subject: "issue-356",
				Now:     now,
				PR:      &PullRequestSnapshot{Number: 42, Open: true, Checks: ChecksFailing},
			},
			wantState:  StatePRChecksFailing,
			wantAction: ActionCommentBlocker,
		},
		{
			name: "open PR with actionable review feedback comments blocker",
			obs: Observation{
				Subject: "issue-356",
				Now:     now,
				PR:      &PullRequestSnapshot{Number: 42, Open: true, Checks: ChecksPending, Review: ReviewActionable},
			},
			wantState:  StatePRReviewFeedback,
			wantAction: ActionCommentBlocker,
		},
		{
			name: "open PR behind base comments blocker",
			obs: Observation{
				Subject: "issue-356",
				Now:     now,
				PR:      &PullRequestSnapshot{Number: 42, Open: true, Checks: ChecksPending, BranchBehind: true},
			},
			wantState:  StatePRBranchDirty,
			wantAction: ActionCommentBlocker,
		},
		{
			name: "retry exhausted while PR remains open blocks issue",
			obs: Observation{
				Subject: "issue-356",
				Now:     now,
				Worker:  WorkerSnapshot{ID: "worker-exhausted", Attempt: 2, MaxAttempts: 2},
				PR:      &PullRequestSnapshot{Number: 42, Open: true, Checks: ChecksPending},
			},
			wantState:  StateRetryExhaustedOpenPR,
			wantAction: ActionLabelBlocked,
		},
		{
			name: "green PR waits for merge approval and labels ready",
			obs: Observation{
				Subject: "issue-356",
				Now:     now,
				PR:      &PullRequestSnapshot{Number: 42, Open: true, Checks: ChecksGreen, Review: ReviewApproved},
			},
			wantState:  StateReadyForMerge,
			wantAction: ActionLabelReady,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision := Evaluate(tt.obs)
			if decision.State != tt.wantState {
				t.Fatalf("state = %q, want %q", decision.State, tt.wantState)
			}
			if !hasAction(decision.SafeActions, ActionUpdateMissionReason) {
				t.Fatalf("expected mission-control reason update action, got %+v", decision.SafeActions)
			}
			if !hasAction(decision.SafeActions, tt.wantAction) {
				t.Fatalf("missing action %q in %+v", tt.wantAction, decision.SafeActions)
			}
			if tt.wantState == StateReadyForMerge && len(decision.ApprovalActions) != 1 {
				t.Fatalf("expected one merge approval action, got %+v", decision.ApprovalActions)
			}
		})
	}
}

func TestSupervisorNotifiesOncePerStateTransition(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	notifier := &fakeNotifier{}
	sup := New(WithNotifier(notifier))

	stale := Observation{
		Subject:    "issue-356",
		Now:        now,
		StaleAfter: time.Minute,
		Worker: WorkerSnapshot{
			ID:          "worker-stale",
			Running:     true,
			Alive:       true,
			LastLogAt:   now.Add(-2 * time.Minute),
			Attempt:     1,
			MaxAttempts: 2,
		},
	}

	if _, err := sup.Reconcile(context.Background(), []Observation{stale}); err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	if _, err := sup.Reconcile(context.Background(), []Observation{stale}); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if got := len(notifier.decisions); got != 1 {
		t.Fatalf("notifications after repeated stale state = %d, want 1", got)
	}

	failingChecks := Observation{
		Subject: "issue-356",
		Now:     now.Add(time.Minute),
		PR:      &PullRequestSnapshot{Number: 42, Open: true, Checks: ChecksFailing},
	}
	if _, err := sup.Reconcile(context.Background(), []Observation{failingChecks}); err != nil {
		t.Fatalf("third reconcile failed: %v", err)
	}
	if got := len(notifier.decisions); got != 2 {
		t.Fatalf("notifications after state transition = %d, want 2", got)
	}
	if notifier.decisions[1].State != StatePRChecksFailing {
		t.Fatalf("last notification state = %q, want %q", notifier.decisions[1].State, StatePRChecksFailing)
	}
}

func TestSupervisorRecoveryActionsAreIdempotentAcrossPolls(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	actions := &fakeActionRunner{}
	sup := New(WithSafeActionRunner(actions))

	obs := Observation{
		Subject: "issue-356",
		Now:     now,
		Worker: WorkerSnapshot{
			ID:           "worker-dead",
			Running:      false,
			Alive:        false,
			Branch:       "feat/issue-356",
			BranchPushed: true,
		},
	}

	first, err := sup.Reconcile(context.Background(), []Observation{obs})
	if err != nil {
		t.Fatalf("first reconcile failed: %v", err)
	}
	second, err := sup.Reconcile(context.Background(), []Observation{obs})
	if err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if len(first.SafeActions) == 0 {
		t.Fatal("expected safe actions on first transition")
	}
	if len(second.SafeActions) != 0 {
		t.Fatalf("expected no repeated safe actions, got %+v", second.SafeActions)
	}
	if got := actions.count(ActionCreatePR); got != 1 {
		t.Fatalf("create PR action count = %d, want 1", got)
	}
}

func TestSupervisorStatusExposesDecisionAndLastSafeAction(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	sup := New(WithSafeActionRunner(&fakeActionRunner{}))
	obs := Observation{
		Subject: "issue-356",
		Now:     now,
		PR:      &PullRequestSnapshot{Number: 42, Open: true, Checks: ChecksGreen, Review: ReviewApproved},
	}

	if _, err := sup.Reconcile(context.Background(), []Observation{obs}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	status := sup.Status()
	if status.CurrentDecision == nil || status.CurrentDecision.State != StateReadyForMerge {
		t.Fatalf("current decision = %+v, want ready for merge", status.CurrentDecision)
	}
	if status.LastSafeAction == nil || status.LastSafeAction.Action.Kind != ActionLabelReady {
		t.Fatalf("last safe action = %+v, want label ready", status.LastSafeAction)
	}

	status.CurrentDecision.State = StatePRChecksFailing
	status2 := sup.Status()
	if status2.CurrentDecision.State != StateReadyForMerge {
		t.Fatalf("status was not cloned: got %q", status2.CurrentDecision.State)
	}
}

func hasAction(actions []Action, want ActionKind) bool {
	for _, action := range actions {
		if action.Kind == want {
			return true
		}
	}
	return false
}

type fakeNotifier struct {
	decisions []Decision
}

func (f *fakeNotifier) NotifySupervisorDecision(_ context.Context, decision Decision) error {
	f.decisions = append(f.decisions, decision)
	return nil
}

type fakeActionRunner struct {
	actions []Action
}

func (f *fakeActionRunner) RunSafeAction(_ context.Context, _ Decision, action Action) error {
	f.actions = append(f.actions, action)
	return nil
}

func (f *fakeActionRunner) count(kind ActionKind) int {
	count := 0
	for _, action := range f.actions {
		if action.Kind == kind {
			count++
		}
	}
	return count
}
