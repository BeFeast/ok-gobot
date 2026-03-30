package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/tools"
)

func newTestReflectionTracker(t *testing.T) (*ReflectionTracker, string) {
	t.Helper()
	dir := t.TempDir()
	mem := NewMemory(dir)
	tracker := NewReflectionTracker(mem)
	return tracker, dir
}

func TestReflectionTracker_CountsFailures(t *testing.T) {
	tracker, _ := newTestReflectionTracker(t)

	tracker.RecordFailure("my_tool", errors.New("something broke"))
	tracker.RecordFailure("my_tool", errors.New("something broke again"))

	if got := tracker.FailureCount("my_tool"); got != 2 {
		t.Fatalf("expected failure count 2, got %d", got)
	}
}

func TestReflectionTracker_NilErrorIsIgnored(t *testing.T) {
	tracker, _ := newTestReflectionTracker(t)
	tracker.RecordFailure("my_tool", nil)
	if got := tracker.FailureCount("my_tool"); got != 0 {
		t.Fatalf("expected count 0 for nil error, got %d", got)
	}
}

func TestReflectionTracker_IndependentCountsPerTool(t *testing.T) {
	tracker, _ := newTestReflectionTracker(t)

	tracker.RecordFailure("tool_a", errors.New("err a"))
	tracker.RecordFailure("tool_b", errors.New("err b"))
	tracker.RecordFailure("tool_b", errors.New("err b2"))

	if got := tracker.FailureCount("tool_a"); got != 1 {
		t.Fatalf("tool_a: expected 1, got %d", got)
	}
	if got := tracker.FailureCount("tool_b"); got != 2 {
		t.Fatalf("tool_b: expected 2, got %d", got)
	}
}

func TestReflectionTracker_Reset(t *testing.T) {
	tracker, _ := newTestReflectionTracker(t)
	tracker.RecordFailure("my_tool", errors.New("err"))
	tracker.Reset()
	if got := tracker.FailureCount("my_tool"); got != 0 {
		t.Fatalf("expected 0 after reset, got %d", got)
	}
}

func TestReflectionTracker_WritesMemoryNote(t *testing.T) {
	tracker, dir := newTestReflectionTracker(t)

	tracker.RecordFailure("bad_tool", errors.New("connection refused"))

	// Give the goroutine time to write.
	deadline := time.Now().Add(2 * time.Second)
	var content string
	for time.Now().Before(deadline) {
		date := time.Now().Format("2006-01-02")
		path := dir + "/memory/" + date + ".md"
		data, err := os.ReadFile(path)
		if err == nil {
			content = string(data)
			if strings.Contains(content, "bad_tool") {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(content, "bad_tool") {
		t.Fatalf("expected daily note to contain 'bad_tool', got:\n%s", content)
	}
	if !strings.Contains(content, "connection refused") {
		t.Fatalf("expected daily note to contain error text, got:\n%s", content)
	}
}

func TestReflectionTracker_ThresholdProposeFix(t *testing.T) {
	tracker, dir := newTestReflectionTracker(t)
	// Trigger exactly at the threshold.
	for i := 0; i < FailureThreshold; i++ {
		tracker.RecordFailure("flaky_tool", errors.New("timeout"))
	}

	// Wait for async writes to complete.
	deadline := time.Now().Add(2 * time.Second)
	var content string
	for time.Now().Before(deadline) {
		date := time.Now().Format("2006-01-02")
		path := dir + "/memory/" + date + ".md"
		data, err := os.ReadFile(path)
		if err == nil {
			content = string(data)
			if strings.Contains(content, "Suggested fix") {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !strings.Contains(content, "Suggested fix") {
		t.Fatalf("expected fix proposal after %d failures, got:\n%s", FailureThreshold, content)
	}
}

// errTool is a tool that always returns a specified error.
type errTool struct {
	toolName string
	errMsg   string
}

func (e *errTool) Name() string                      { return e.toolName }
func (e *errTool) Description() string               { return "error tool for testing" }
func (e *errTool) GetSchema() map[string]interface{} { return nil }
func (e *errTool) Execute(_ context.Context, _ ...string) (string, error) {
	return "", fmt.Errorf("%s", e.errMsg)
}

func TestToolCallingAgent_ReflectorWiredOnToolFailure(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&errTool{toolName: "bad_tool", errMsg: "injected failure"})

	mockAI := &mockAIClient{
		toolCallName: "bad_tool",
		toolCallArgs: `{"input":"x"}`,
		finalText:    "done",
	}

	mem := NewMemory(t.TempDir())
	tracker := NewReflectionTracker(mem)

	a := NewToolCallingAgent(mockAI, reg, &Personality{
		Files: map[string]string{"IDENTITY.md": "Test Bot"},
	})
	a.SetReflectionTracker(tracker)

	_, err := a.ProcessRequest(context.Background(), "trigger failure", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Allow goroutine to increment the counter.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tracker.FailureCount("bad_tool") > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := tracker.FailureCount("bad_tool"); got == 0 {
		t.Fatal("expected reflector to record tool failure")
	}
}
