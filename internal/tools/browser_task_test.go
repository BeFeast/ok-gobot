package tools

import (
	"context"
	"strings"
	"testing"

	"ok-gobot/internal/delegation"
)

type recordingSubagentSubmitter struct {
	called bool
}

func (s *recordingSubagentSubmitter) SubmitAndWait(context.Context, int64, string, delegation.Job) (string, error) {
	s.called = true
	return "ok", nil
}

func TestBrowserTaskRejectsImplementationMutationBeforeSpawning(t *testing.T) {
	submitter := &recordingSubagentSubmitter{}
	tool := NewBrowserTaskTool(submitter, 123)

	_, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"task": "Fix the video-summary implementation and deploy the service",
	})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only error, got %v", err)
	}
	if submitter.called {
		t.Fatal("implementation mutation task reached subagent submitter")
	}
}

func TestBrowserTaskAllowsReadOnlyResearch(t *testing.T) {
	submitter := &recordingSubagentSubmitter{}
	tool := NewBrowserTaskTool(submitter, 123)

	result, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"task": "Open the official release notes and extract the latest formatting changes",
	})
	if err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if result != "ok" || !submitter.called {
		t.Fatalf("result=%q called=%v; want ok, true", result, submitter.called)
	}
}
