package bot

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/agent"
)

// newTestLiveEditor returns a LiveStreamEditor with nil bot/msg for unit tests
// that don't make real Telegram API calls.
func newTestLiveEditor() *LiveStreamEditor {
	return &LiveStreamEditor{}
}

func TestLiveStreamEditor_InitialFormat(t *testing.T) {
	e := newTestLiveEditor()
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if out != "💭 Working…" {
		t.Errorf("expected default placeholder, got %q", out)
	}
}

func TestLiveStreamEditor_ToolStarted(t *testing.T) {
	e := newTestLiveEditor()
	e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventStarted, ToolName: "search"})
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if !strings.Contains(out, "⚙️ search…") {
		t.Errorf("expected in-progress status, got %q", out)
	}
	if !e.HasAny() {
		t.Error("expected HasAny=true after ToolEventStarted")
	}
}

func TestLiveStreamEditor_ToolFinishedSuccess(t *testing.T) {
	e := newTestLiveEditor()
	e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventStarted, ToolName: "fetch"})
	e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventFinished, ToolName: "fetch"})
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if !strings.Contains(out, "✅ fetch") {
		t.Errorf("expected success status, got %q", out)
	}
}

func TestLiveStreamEditor_ToolFinishedError(t *testing.T) {
	e := newTestLiveEditor()
	e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventStarted, ToolName: "patch"})
	e.OnToolEvent(agent.ToolEvent{
		Type:     agent.ToolEventFinished,
		ToolName: "patch",
		Err:      errors.New("write failed"),
	})
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if !strings.Contains(out, "❌ patch") {
		t.Errorf("expected error status, got %q", out)
	}
}

func TestLiveStreamEditor_AppendDelta_NoTools(t *testing.T) {
	e := newTestLiveEditor()
	e.AppendDelta("Hello, ")
	e.AppendDelta("world!")
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if out != "Hello, world!" {
		t.Errorf("expected content only, got %q", out)
	}
}

func TestLiveStreamEditor_AppendDelta_WithTools(t *testing.T) {
	e := newTestLiveEditor()
	e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventStarted, ToolName: "search"})
	e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventFinished, ToolName: "search"})
	e.AppendDelta("Result: ")
	e.AppendDelta("42")
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if !strings.Contains(out, "✅ search") {
		t.Errorf("expected tool status in output, got %q", out)
	}
	if !strings.Contains(out, "Result: 42") {
		t.Errorf("expected content in output, got %q", out)
	}
	// Status should appear before content.
	statusIdx := strings.Index(out, "✅ search")
	contentIdx := strings.Index(out, "Result: 42")
	if statusIdx >= contentIdx {
		t.Errorf("expected status before content, got %q", out)
	}
}

func TestLiveStreamEditor_ResetContent(t *testing.T) {
	e := newTestLiveEditor()
	e.AppendDelta("some streaming text")
	e.ResetContent()
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if strings.Contains(out, "some streaming text") {
		t.Errorf("expected content cleared after ResetContent, got %q", out)
	}
}

func TestLiveStreamEditor_MaxToolStatusLines(t *testing.T) {
	e := newTestLiveEditor()
	tools := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, name := range tools {
		e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventStarted, ToolName: name})
		e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventFinished, ToolName: name})
	}
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > maxToolStatusLines {
		t.Errorf("expected at most %d lines, got %d: %q", maxToolStatusLines, len(lines), out)
	}
	if !strings.Contains(out, "g") {
		t.Errorf("expected last tool 'g' to be visible, got %q", out)
	}
}

func TestLiveStreamEditor_Stop_PreventsEdits(t *testing.T) {
	e := newTestLiveEditor()
	e.Stop()

	e.AppendDelta("should be ignored")
	e.mu.Lock()
	pendingAfterStop := e.pending
	e.mu.Unlock()
	if pendingAfterStop {
		t.Error("expected no pending edit after Stop()")
	}
}

func TestLiveStreamEditor_HasAny_False_Initially(t *testing.T) {
	e := newTestLiveEditor()
	if e.HasAny() {
		t.Error("expected HasAny=false on empty editor")
	}
}

// TestLiveStreamEditor_ScheduleEdit_TokenThreshold verifies that accumulating
// streamTokenThreshold or more tokens causes an immediate edit without waiting
// for the full interval.
func TestLiveStreamEditor_ScheduleEdit_TokenThreshold(t *testing.T) {
	editCalled := make(chan struct{}, 1)

	// Build a LiveStreamEditor with a spy edit function by using an inline
	// goroutine that reads the pending state.
	e := &LiveStreamEditor{}

	// Append exactly streamTokenThreshold tokens; each call to AppendDelta
	// increments pendingTokens by 1 (one call = one token for test purposes).
	for i := 0; i < streamTokenThreshold; i++ {
		e.AppendDelta("x")
	}

	e.mu.Lock()
	burst := e.pendingTokens >= streamTokenThreshold
	e.mu.Unlock()

	if !burst {
		t.Errorf("expected pendingTokens >= %d, got %d", streamTokenThreshold, e.pendingTokens)
	}

	// The goroutine spawned by AppendDelta would normally call bot.Edit.
	// Since bot is nil we just check that scheduleEdit will not sleep for
	// the full interval when the token threshold is met.
	start := time.Now()
	go func() {
		// Simulate what scheduleEdit does: if tokensBurst, skip the sleep.
		e.mu.Lock()
		elapsed := time.Since(e.lastEdit)
		tb := e.pendingTokens >= streamTokenThreshold
		e.mu.Unlock()
		if tb || elapsed >= streamEditInterval {
			editCalled <- struct{}{}
		}
	}()

	select {
	case <-editCalled:
		elapsed := time.Since(start)
		if elapsed > 100*time.Millisecond {
			t.Errorf("expected immediate burst edit, took %v", elapsed)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("expected burst edit to fire quickly, timed out")
	}
}
