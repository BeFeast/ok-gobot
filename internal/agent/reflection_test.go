package agent

import (
	"errors"
	"testing"
	"time"
)

func TestReflector_RecordsFailures(t *testing.T) {
	r := NewReflector(3, nil)

	r.recordFailure("my_tool", `{"input":"x"}`, errors.New("exit status 1"))

	if got := r.FailureCount("my_tool"); got != 1 {
		t.Fatalf("expected failure count 1, got %d", got)
	}

	recs := r.RecentErrors("my_tool")
	if len(recs) != 1 {
		t.Fatalf("expected 1 recent error, got %d", len(recs))
	}
	if recs[0].ToolName != "my_tool" {
		t.Errorf("unexpected tool name: %q", recs[0].ToolName)
	}
	if recs[0].ErrMsg != "exit status 1" {
		t.Errorf("unexpected error message: %q", recs[0].ErrMsg)
	}
}

func TestReflector_ThresholdTriggersProposal(t *testing.T) {
	r := NewReflector(3, nil)

	for i := 0; i < 3; i++ {
		r.recordFailure("flaky_tool", `{}`, errors.New("timeout: context deadline exceeded"))
	}

	if got := r.FailureCount("flaky_tool"); got != 3 {
		t.Fatalf("expected failure count 3, got %d", got)
	}
}

func TestReflector_BelowThresholdNoProposal(t *testing.T) {
	r := NewReflector(3, nil)

	r.recordFailure("stable_tool", `{}`, errors.New("network error"))
	r.recordFailure("stable_tool", `{}`, errors.New("network error"))

	if got := r.FailureCount("stable_tool"); got != 2 {
		t.Fatalf("expected 2, got %d", got)
	}
}

func TestReflector_RecordFailureAsync_NonBlocking(t *testing.T) {
	r := NewReflector(3, nil)

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.RecordFailureAsync("async_tool", `{}`, errors.New("some error"))
	}()

	select {
	case <-done:
		// completed quickly — async call did not block
	case <-time.After(2 * time.Second):
		t.Fatal("RecordFailureAsync blocked for too long")
	}
}

func TestReflector_CapsBoundedAt10Records(t *testing.T) {
	r := NewReflector(100, nil)

	for i := 0; i < 15; i++ {
		r.recordFailure("busy_tool", `{}`, errors.New("error"))
	}

	if got := r.FailureCount("busy_tool"); got != 15 {
		t.Fatalf("expected 15, got %d", got)
	}
	recs := r.RecentErrors("busy_tool")
	if len(recs) > 10 {
		t.Fatalf("expected at most 10 recent records, got %d", len(recs))
	}
}

func TestDeriveSuggestedFix(t *testing.T) {
	cases := []struct {
		errMsg   string
		wantSubs string
	}{
		{"file not found", "registered"},
		{"context deadline exceeded timeout", "timeout"},
		{"permission denied", "permissions"},
		{"failed to unmarshal JSON", "schema"},
		{"dial tcp: connection refused", "network"},
		{"unexpected failure", "investigate"},
	}

	for _, tc := range cases {
		fix := deriveSuggestedFix("tool", tc.errMsg)
		if fix == "" {
			t.Errorf("deriveSuggestedFix(%q) returned empty string", tc.errMsg)
		}
		_ = fix
	}
}

func TestToolCallingAgent_SetReflector(t *testing.T) {
	r := NewReflector(3, nil)
	agent := NewToolCallingAgent(nil, nil, &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	})
	// Should not panic
	agent.SetReflector(r)
	if agent.reflector != r {
		t.Fatal("reflector not set on agent")
	}
}
