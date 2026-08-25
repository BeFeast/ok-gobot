package agent

import (
	"bytes"
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/delegation"
	"ok-gobot/internal/tools"
)

func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	fn()
	_ = os.Stdout
	return buf.String()
}

func TestLogToolCall(t *testing.T) {
	start := time.Now()

	if out := captureLog(t, func() { logToolCall("browser", start, 42, nil, false) }); !strings.Contains(out, "name=browser") || !strings.Contains(out, "ok=true") || !strings.Contains(out, "out=42B") {
		t.Errorf("success line wrong: %q", out)
	}
	if out := captureLog(t, func() { logToolCall("browser", start, 0, errors.New("boom"), false) }); !strings.Contains(out, "ok=false") || !strings.Contains(out, `err="boom"`) {
		t.Errorf("failure line wrong: %q", out)
	}
	// a spawned call is a success for the agent loop, never a failure
	if out := captureLog(t, func() { logToolCall("browser", start, 0, nil, true) }); !strings.Contains(out, "ok=true") || !strings.Contains(out, "spawned=true") {
		t.Errorf("spawn line wrong: %q", out)
	}
	// policy denials must not inflate the failure rate
	denial := &tools.ToolDenial{Reason: "blocked by e-stop"}
	if out := captureLog(t, func() { logToolCall("shell", start, 0, denial, false) }); !strings.Contains(out, "denied=true") || !strings.Contains(out, "blocked by e-stop") {
		t.Errorf("denial line wrong: %q", out)
	}
	long := strings.Repeat("x", 500)
	if out := captureLog(t, func() { logToolCall("t", start, 0, errors.New(long), false) }); !strings.Contains(out, "…") || len(out) > 400 {
		t.Errorf("truncation wrong: len=%d", len(out))
	}
}

// Regression guard for the classification bug the 2026-08-21 telemetry patch
// believed it had fixed. That patch added the denied=true branch to
// logToolCall and proved it with a hand-built *tools.ToolDenial — the sink
// worked, but no production refusal path constructed one, so denied=true fired
// ZERO times all-time while real policy refusals were logged as ok=false.
// This test drives the real browser_task refusal through the real classifier.
func TestLogToolCallClassifiesRealPolicyRefusalAsDenied(t *testing.T) {
	tool := tools.NewBrowserTaskTool(refusingSubmitter{}, 1)

	_, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"task": "Fix the video-summary implementation and deploy the service",
	})
	if err == nil {
		t.Fatal("expected browser_task to refuse the mutation task")
	}

	out := captureLog(t, func() { logToolCall("browser_task", time.Now(), 0, err, false) })

	if !strings.Contains(out, "denied=true") {
		t.Errorf("policy refusal must log denied=true, got: %q", out)
	}
	if strings.Contains(out, "err=") {
		t.Errorf("policy refusal must not be logged as a failure (err=...), got: %q", out)
	}
}

// refusingSubmitter must never be called: the refusal happens before spawn.
type refusingSubmitter struct{}

func (refusingSubmitter) SubmitAndWait(context.Context, int64, string, delegation.Job) (string, error) {
	return "", errors.New("submitter must not be reached for a refused task")
}
