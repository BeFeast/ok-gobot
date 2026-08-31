package tools

import (
	"context"
	"strings"
	"testing"
	"time"

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

// jobCapturingSubmitter records the delegated-run contract it was handed.
type jobCapturingSubmitter struct {
	called bool
	prompt string
	job    delegation.Job
}

func (s *jobCapturingSubmitter) SubmitAndWait(_ context.Context, _ int64, task string, job delegation.Job) (string, error) {
	s.called = true
	s.prompt = task
	s.job = job
	return "ok", nil
}

// A policy refusal is a deliberate decision, not a malfunction. It must carry
// the *ToolDenial type so telemetry classifies it as denied=true instead of
// inflating the failure rate — measured 2026-08-24: browser_task read as
// 4/4 failed all-time, but 2 of those 4 were this refusal.
func TestBrowserTaskReadOnlyRefusalIsTypedDenial(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*BrowserTaskTool) error
	}{
		{"ExecuteJSON", func(tool *BrowserTaskTool) error {
			_, err := tool.ExecuteJSON(context.Background(), map[string]string{
				"task": "Fix the video-summary implementation and deploy the service",
			})
			return err
		}},
		{"Execute", func(tool *BrowserTaskTool) error {
			_, err := tool.Execute(context.Background(), "Fix the video-summary implementation and deploy the service")
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			submitter := &recordingSubagentSubmitter{}
			err := tc.call(NewBrowserTaskTool(submitter, 123))
			if err == nil {
				t.Fatal("expected refusal, got nil error")
			}
			denial, ok := IsToolDenial(err)
			if !ok {
				t.Fatalf("policy refusal must be a *ToolDenial so telemetry reports denied=true, got %T: %v", err, err)
			}
			if denial.ToolName != "browser_task" {
				t.Errorf("ToolName = %q, want browser_task", denial.ToolName)
			}
			if denial.Family == "" || denial.Remediation == "" {
				t.Errorf("denial must carry Family and Remediation for the rendering layers: %+v", denial)
			}
			if !strings.Contains(denial.Reason, "read-only") {
				t.Errorf("Reason = %q, want it to explain the read-only refusal", denial.Reason)
			}
			if submitter.called {
				t.Fatal("implementation mutation task reached subagent submitter")
			}
		})
	}
}

// The delegated browser budget has two stops: a clock (MaxDuration) and a call
// counter (MaxToolCalls). They must be consistent, i.e. the counter must be
// able to absorb a full-length run at the pace browser runs actually keep, so
// the clock is what stops a long job. Measured on the production journal
// 2026-08-24 14:55:07-14:58:23: 50 browser calls in 203.6s = 4.07s per call
// (median 4s, p90 6s); only 40.8s of that was in-tool time, the rest was model
// round-trips. Under the old ceiling of 50 the counter always won and the
// 10-minute budget was never once reached.
func TestBrowserTaskBudgetsAreMutuallyConsistent(t *testing.T) {
	submitter := &jobCapturingSubmitter{}
	tool := NewBrowserTaskTool(submitter, 123)

	if _, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"task": "Open the official release notes and extract the latest formatting changes",
	}); err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if !submitter.called {
		t.Fatal("submitter never received a job")
	}

	// Measured mean seconds per browser tool call in a real delegated run.
	const observedPacePerCall = 4.07 * float64(time.Second)

	job := submitter.job
	reachable := float64(job.MaxToolCalls) * observedPacePerCall
	if reachable < float64(job.MaxDuration) {
		t.Errorf("call ceiling %d cannot reach the %s clock at the measured %.2fs/call pace "+
			"(covers only %.0fs); the counter would bind first and MaxDuration would be dead config",
			job.MaxToolCalls, job.MaxDuration, observedPacePerCall/float64(time.Second),
			reachable/float64(time.Second))
	}
	if job.MaxDuration != 10*time.Minute {
		t.Errorf("MaxDuration = %v, want 10m", job.MaxDuration)
	}
}

func TestBrowserTaskWorkerPromptStopsCSSClickLoops(t *testing.T) {
	submitter := &jobCapturingSubmitter{}
	tool := NewBrowserTaskTool(submitter, 123)

	if _, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"task": "Open the official release notes and extract the latest formatting changes",
	}); err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if !submitter.called {
		t.Fatal("submitter never received a job")
	}

	for _, want := range []string{
		"Prefer snapshot and page text over clicking",
		"As soon as you have extracted matching listings, prices, or the requested facts from a live page, return them",
		"Do not guess CSS selectors",
		"do not retry that selector",
		"After two failed interactions, stop with NOT_FOUND",
		"Read that first. If ax_error is set, do not retry snapshot",
		"browser text with no selector dumps the visible page text",
	} {
		if !strings.Contains(submitter.prompt, want) {
			t.Errorf("worker prompt missing %q\n%s", want, submitter.prompt)
		}
	}
}
