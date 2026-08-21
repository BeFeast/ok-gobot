package agent

import (
	"bytes"
	"errors"
	"log"
	"os"
	"strings"
	"testing"
	"time"

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
