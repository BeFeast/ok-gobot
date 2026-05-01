package supervisor

import (
	"context"
	"testing"
	"time"

	"ok-gobot/internal/prhygiene"
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
			name: "open PR with dirty merge state comments blocker",
			obs: Observation{
				Subject: "issue-242",
				Now:     now,
				PR:      &PullRequestSnapshot{Number: 242, Open: true, State: "OPEN", Checks: ChecksPending, MergeState: "DIRTY"},
			},
			wantState:  StatePRBranchDirty,
			wantAction: ActionCommentBlocker,
		},
		{
			name: "stale open PR refreshes metadata without write action",
			obs: Observation{
				Subject:      "issue-279",
				Now:          now,
				PRStaleAfter: 14 * 24 * time.Hour,
				PR: &PullRequestSnapshot{
					Number:     279,
					Open:       true,
					State:      "OPEN",
					Checks:     ChecksGreen,
					Review:     ReviewApproved,
					MergeState: "CLEAN",
					UpdatedAt:  now.Add(-31 * 24 * time.Hour),
				},
			},
			wantState:  StatePRStale,
			wantAction: ActionRefreshMetadata,
		},
		{
			name: "open PR with Greptile findings refreshes metadata",
			obs: Observation{
				Subject: "issue-303",
				Now:     now,
				PR:      &PullRequestSnapshot{Number: 303, Open: true, State: "OPEN", GreptileFindings: true},
			},
			wantState:  StatePRGreptileFindings,
			wantAction: ActionRefreshMetadata,
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
			if isPRBlockerState(tt.wantState) && decision.PRBlocker == nil {
				t.Fatalf("expected PR blocker metadata for %q", tt.wantState)
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
	if status.CurrentDecisions["issue-356"].State != StateReadyForMerge {
		t.Fatalf("current decisions = %+v, want issue-356 ready for merge", status.CurrentDecisions)
	}
	if len(status.PRBlockers) != 0 {
		t.Fatalf("PR blockers = %+v, want none for ready PR", status.PRBlockers)
	}

	status.CurrentDecision.State = StatePRChecksFailing
	status.CurrentDecisions["issue-356"] = Decision{State: StatePRChecksFailing, Subject: "issue-356"}
	status.PRBlockers = append(status.PRBlockers, prhygiene.Blocker{Number: 1})
	status2 := sup.Status()
	if status2.CurrentDecision.State != StateReadyForMerge {
		t.Fatalf("status was not cloned: got %q", status2.CurrentDecision.State)
	}
	if status2.CurrentDecisions["issue-356"].State != StateReadyForMerge {
		t.Fatalf("status decision map was not cloned: got %q", status2.CurrentDecisions["issue-356"].State)
	}
	if len(status2.PRBlockers) != 0 {
		t.Fatalf("status PR blocker slice was not cloned: %+v", status2.PRBlockers)
	}
}

func TestSupervisorStatusExposesStalePRBlocker(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	sup := New()
	obs := Observation{
		Subject:      "issue-279",
		Now:          now,
		PRStaleAfter: 14 * 24 * time.Hour,
		PR: &PullRequestSnapshot{
			Number:    279,
			Open:      true,
			State:     "OPEN",
			Title:     "test(agent): expand reflection test coverage (#243)",
			URL:       "https://github.com/BeFeast/ok-gobot/pull/279",
			Checks:    ChecksGreen,
			Review:    ReviewApproved,
			UpdatedAt: time.Date(2026, 3, 30, 20, 20, 23, 0, time.UTC),
		},
	}

	if _, err := sup.Reconcile(context.Background(), []Observation{obs}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	status := sup.Status()
	if len(status.PRBlockers) != 1 {
		t.Fatalf("PR blockers = %+v, want one", status.PRBlockers)
	}
	blocker := status.PRBlockers[0]
	if blocker.Number != 279 || blocker.Kind != prhygiene.KindStale || blocker.State != "OPEN" || blocker.UpdatedAt.IsZero() {
		t.Fatalf("unexpected stale PR blocker: %+v", blocker)
	}
	if status.CurrentDecision == nil || status.CurrentDecision.PRBlocker == nil || status.CurrentDecision.PRBlocker.Number != 279 {
		t.Fatalf("current decision missing PR blocker: %+v", status.CurrentDecision)
	}
}

func TestSupervisorStatusKeepsStuckDecisionWhenHealthySubjectFollows(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	sup := New()

	stuck := Observation{
		Subject: "issue-stuck",
		Now:     now,
		PR:      &PullRequestSnapshot{Number: 42, Open: true, Checks: ChecksFailing},
	}
	healthy := Observation{
		Subject: "issue-healthy",
		Now:     now,
	}

	result, err := sup.Reconcile(context.Background(), []Observation{stuck, healthy})
	if err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if len(result.Decisions) != 1 {
		t.Fatalf("decisions = %+v, want only the stuck subject", result.Decisions)
	}

	status := sup.Status()
	if status.CurrentDecision == nil || status.CurrentDecision.Subject != "issue-stuck" || status.CurrentDecision.State != StatePRChecksFailing {
		t.Fatalf("current decision = %+v, want stuck issue", status.CurrentDecision)
	}
	if len(status.CurrentDecisions) != 1 || status.CurrentDecisions["issue-stuck"].State != StatePRChecksFailing {
		t.Fatalf("current decisions = %+v, want only stuck issue", status.CurrentDecisions)
	}

	if _, err := sup.Reconcile(context.Background(), []Observation{{Subject: "issue-stuck", Now: now.Add(time.Minute)}}); err != nil {
		t.Fatalf("healthy reconcile failed: %v", err)
	}
	status = sup.Status()
	if status.CurrentDecision == nil || status.CurrentDecision.Subject != "issue-stuck" || status.CurrentDecision.State != StateNone {
		t.Fatalf("current decision after recovery = %+v, want none for issue-stuck", status.CurrentDecision)
	}
	if len(status.CurrentDecisions) != 0 {
		t.Fatalf("current decisions after recovery = %+v, want empty", status.CurrentDecisions)
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

func isPRBlockerState(state StuckState) bool {
	switch state {
	case StatePRChecksFailing, StatePRReviewFeedback, StatePRGreptileFindings, StatePRBranchDirty, StatePRStale:
		return true
	default:
		return false
	}
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
