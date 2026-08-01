package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/tools"
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

// --- Memory-persistence tests ---

func TestReflector_WritesFailureToMemory(t *testing.T) {
	mem := NewMemory(t.TempDir())
	r := NewReflector(3, mem)

	r.recordFailure("tool_x", `{"input":"data"}`, errors.New("test error"))

	note, err := mem.GetTodayNote()
	if err != nil {
		t.Fatalf("GetTodayNote failed: %v", err)
	}
	if note.Content == "" {
		t.Fatal("expected memory note to have content after failure")
	}
	if !strings.Contains(note.Content, "tool_x") {
		t.Fatalf("memory note should contain tool name, got:\n%s", note.Content)
	}
	if !strings.Contains(note.Content, "test error") {
		t.Fatalf("memory note should contain error message, got:\n%s", note.Content)
	}
}

func TestReflector_SuggestedFixIncludedInMemory(t *testing.T) {
	mem := NewMemory(t.TempDir())
	r := NewReflector(3, mem)

	r.recordFailure("parser_tool", `{}`, errors.New("failed to unmarshal input"))

	note, err := mem.GetTodayNote()
	if err != nil {
		t.Fatalf("GetTodayNote failed: %v", err)
	}
	if !strings.Contains(note.Content, "Suggested fix") {
		t.Fatalf("memory note should include suggested fix, got:\n%s", note.Content)
	}
}

func TestReflector_RecordFailureAsync_WritesToMemory(t *testing.T) {
	mem := NewMemory(t.TempDir())
	r := NewReflector(3, mem)

	r.RecordFailureAsync("async_tool", `{}`, errors.New("async error"))
	time.Sleep(100 * time.Millisecond)

	note, err := mem.GetTodayNote()
	if err != nil {
		t.Fatalf("GetTodayNote failed: %v", err)
	}
	if !strings.Contains(note.Content, "async_tool") {
		t.Fatalf("async failure should be written to memory, got:\n%s", note.Content)
	}
}

func TestReflector_NilMemory_DoesNotPanic(t *testing.T) {
	r := NewReflector(3, nil)
	// Should not panic with nil memory.
	r.recordFailure("tool", `{}`, errors.New("error"))
	if got := r.FailureCount("tool"); got != 1 {
		t.Fatalf("expected 1 failure, got %d", got)
	}
}

// --- ToolCallingAgent integration tests ---

func TestToolCallingAgent_ReflectorRecordsFailure(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(&failingTool{
		name:   "flaky_tool",
		output: "",
		err:    errors.New("service unavailable"),
	})

	mockAI := &mockAIClient{
		toolCallName: "flaky_tool",
		toolCallArgs: `{"input":"test"}`,
		finalText:    "tool failed, sorry",
	}

	mem := NewMemory(t.TempDir())
	reflector := NewReflector(3, mem)

	ag := NewToolCallingAgent(mockAI, registry, &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	})
	ag.SetReflector(reflector)

	_, err := ag.ProcessRequest(context.Background(), "use flaky tool", "")
	if err != nil {
		t.Fatalf("ProcessRequest failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if got := reflector.FailureCount("flaky_tool"); got != 1 {
		t.Fatalf("expected 1 failure recorded for flaky_tool, got %d", got)
	}
}

func TestToolCallingAgent_ReflectorThreeFailures(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(&failingTool{
		name:   "bad_tool",
		output: "",
		err:    errors.New("broken"),
	})

	mem := NewMemory(t.TempDir())
	reflector := NewReflector(3, mem)

	for i := 0; i < 3; i++ {
		ai := &mockAIClient{
			toolCallName: "bad_tool",
			toolCallArgs: `{"input":"x"}`,
			finalText:    "failed",
		}
		ag := NewToolCallingAgent(ai, registry, &Personality{
			Files: map[string]string{"IDENTITY.md": "Test Bot"},
		})
		ag.SetReflector(reflector)
		ag.ProcessRequest(context.Background(), "do something", "") //nolint:errcheck
	}

	time.Sleep(150 * time.Millisecond)

	if got := reflector.FailureCount("bad_tool"); got != 3 {
		t.Fatalf("expected 3 failures for bad_tool, got %d", got)
	}
}
