package control_test

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/control"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/supervisor"
	"ok-gobot/internal/tools"
)

type missionSupervisorState struct {
	*mockTUIState
	store *storage.Store
}

func (m *missionSupervisorState) GetStore() *storage.Store { return m.store }

func (m *missionSupervisorState) GetAgentRegistry() *agent.AgentRegistry { return nil }

func (m *missionSupervisorState) GetScheduler() tools.CronScheduler { return nil }

func (m *missionSupervisorState) GetMemoryStatus(context.Context) (memory.IndexStatus, error) {
	return memory.IndexStatus{}, nil
}

func TestMissionSupervisorEndpointExposesDecisionAndLastAction(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "mission-supervisor.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close() //nolint:errcheck

	status := supervisor.Status{
		CurrentDecision: &supervisor.Decision{
			State:      supervisor.StateWaitingMergeApproval,
			TargetKind: "pr",
			TargetID:   "356",
			Reason:     "checks are green and merge approval is required",
		},
		LastSafeAction: &supervisor.ActionRecord{
			DecisionState: supervisor.StateWaitingMergeApproval,
			Action: supervisor.Action{
				Type:       supervisor.ActionLabelIssueReady,
				TargetKind: "issue",
				TargetID:   "356",
				Label:      "ready",
				Reason:     "PR is green and waiting for merge approval",
			},
			AppliedAt: "2026-05-01T12:00:00Z",
		},
	}
	if err := store.SetSupervisorStatus(status); err != nil {
		t.Fatalf("SetSupervisorStatus: %v", err)
	}

	state := &missionSupervisorState{mockTUIState: &mockTUIState{}, store: store}
	_, addr, cancel := startServerWithStore(t, state, store)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/api/mission/supervisor")
	if err != nil {
		t.Fatalf("GET supervisor status: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var got supervisor.Status
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.CurrentDecision == nil || got.CurrentDecision.State != supervisor.StateWaitingMergeApproval {
		t.Fatalf("current decision = %+v, want waiting merge approval", got.CurrentDecision)
	}
	if got.LastSafeAction == nil || got.LastSafeAction.Action.Type != supervisor.ActionLabelIssueReady {
		t.Fatalf("last safe action = %+v, want label issue ready", got.LastSafeAction)
	}
	if got.LastSafeAction.Action.Label != "ready" {
		t.Fatalf("last safe action label = %q, want ready", got.LastSafeAction.Action.Label)
	}

	var _ control.MissionProvider = state
}
