package storage

import (
	"path/filepath"
	"testing"

	"ok-gobot/internal/supervisor"
)

func TestSupervisorStatusPersistsAcrossReopen(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "supervisor.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}

	status := supervisor.Status{
		CurrentDecision: &supervisor.Decision{
			State:      supervisor.StatePRChecksFailing,
			TargetKind: "pr",
			TargetID:   "356",
			Reason:     "CI checks are failing",
		},
		LastSafeAction: &supervisor.ActionRecord{
			DecisionState: supervisor.StatePRChecksFailing,
			Action: supervisor.Action{
				Type:       supervisor.ActionCommentBlocker,
				TargetKind: "pr",
				TargetID:   "356",
				Reason:     "CI checks are failing",
			},
			AppliedAt: "2026-05-01T12:00:00Z",
		},
		TransitionKeys: map[string]string{"pr:356": "pr:356:pr_checks_failing"},
		AppliedActions: map[string]string{"pr:356:pr_checks_failing:comment_blocker:pr:356:": "2026-05-01T12:00:00Z"},
	}
	if err := store.SetSupervisorStatus(status); err != nil {
		t.Fatalf("SetSupervisorStatus() failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() reopen failed: %v", err)
	}
	defer reopened.Close() //nolint:errcheck

	got, err := reopened.GetSupervisorStatus()
	if err != nil {
		t.Fatalf("GetSupervisorStatus() failed: %v", err)
	}
	if got.CurrentDecision == nil || got.CurrentDecision.State != supervisor.StatePRChecksFailing {
		t.Fatalf("current decision = %+v, want PR checks failing", got.CurrentDecision)
	}
	if got.LastSafeAction == nil || got.LastSafeAction.Action.Type != supervisor.ActionCommentBlocker {
		t.Fatalf("last safe action = %+v, want comment blocker", got.LastSafeAction)
	}
}
