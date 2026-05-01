package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/supervisor"
	"ok-gobot/internal/tools"
)

func TestMissionSupervisorReturnsCurrentDecisionAndLastSafeAction(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	provider := fakeMissionSupervisorProvider{
		status: supervisor.Status{
			CurrentDecision: &supervisor.Decision{
				State:     supervisor.StatePRChecksFailing,
				Subject:   "issue-356",
				PRNumber:  42,
				Reason:    "PR #42 has failing checks",
				PRBlocker: &supervisor.PullRequestBlocker{Number: 42, State: "OPEN", Kind: supervisor.PRBlockerKindCI, Reason: "ci checks failing", UpdatedAt: now},
				DecidedAt: now,
			},
			PRBlockers: []supervisor.PullRequestBlocker{{Number: 42, State: "OPEN", Kind: supervisor.PRBlockerKindCI, Reason: "ci checks failing", UpdatedAt: now}},
			LastSafeAction: &supervisor.ActionRecord{
				Subject:   "issue-356",
				State:     supervisor.StatePRChecksFailing,
				Action:    supervisor.Action{Kind: supervisor.ActionCommentBlocker, Target: "#42"},
				CreatedAt: now,
			},
			UpdatedAt: now,
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/mission/supervisor", nil)
	w := httptest.NewRecorder()
	missionSupervisor(provider)(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", w.Code)
	}
	var got supervisor.Status
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.CurrentDecision == nil || got.CurrentDecision.State != supervisor.StatePRChecksFailing {
		t.Fatalf("current decision = %+v, want failing checks", got.CurrentDecision)
	}
	if got.LastSafeAction == nil || got.LastSafeAction.Action.Kind != supervisor.ActionCommentBlocker {
		t.Fatalf("last safe action = %+v, want comment blocker", got.LastSafeAction)
	}
	if len(got.PRBlockers) != 1 || got.PRBlockers[0].Number != 42 || got.PRBlockers[0].Kind != supervisor.PRBlockerKindCI {
		t.Fatalf("PR blockers = %+v, want CI blocker for #42", got.PRBlockers)
	}
}

type fakeMissionSupervisorProvider struct {
	status supervisor.Status
}

func (f fakeMissionSupervisorProvider) GetStore() *storage.Store { return nil }

func (f fakeMissionSupervisorProvider) GetAgentRegistry() *agent.AgentRegistry { return nil }

func (f fakeMissionSupervisorProvider) GetScheduler() tools.CronScheduler { return nil }

func (f fakeMissionSupervisorProvider) GetMemoryStatus(context.Context) (memory.IndexStatus, error) {
	return memory.IndexStatus{}, nil
}

func (f fakeMissionSupervisorProvider) GetSupervisorStatus() supervisor.Status {
	return f.status
}
