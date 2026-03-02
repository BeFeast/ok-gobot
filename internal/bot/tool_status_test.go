package bot

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/agent"
)

func TestToolStatusTracker_Empty(t *testing.T) {
	var tr ToolStatusTracker
	out := tr.Format()
	if out != "💭 Working…" {
		t.Errorf("expected default placeholder, got %q", out)
	}
	if tr.HasAny() {
		t.Error("expected HasAny=false on empty tracker")
	}
}

func TestToolStatusTracker_StartedLine(t *testing.T) {
	var tr ToolStatusTracker
	tr.OnStarted("search")
	out := tr.Format()
	if !strings.Contains(out, "⚙️ search…") {
		t.Errorf("expected in-progress line, got %q", out)
	}
	if !tr.HasAny() {
		t.Error("expected HasAny=true after OnStarted")
	}
}

func TestToolStatusTracker_FinishedSuccess(t *testing.T) {
	var tr ToolStatusTracker
	tr.OnStarted("search")
	tr.OnFinished("search", false)
	out := tr.Format()
	if !strings.Contains(out, "✅ search") {
		t.Errorf("expected success line, got %q", out)
	}
}

func TestToolStatusTracker_FinishedError(t *testing.T) {
	var tr ToolStatusTracker
	tr.OnStarted("patch")
	tr.OnFinished("patch", true)
	out := tr.Format()
	if !strings.Contains(out, "❌ patch") {
		t.Errorf("expected error line, got %q", out)
	}
}

func TestToolStatusTracker_MaxLines(t *testing.T) {
	var tr ToolStatusTracker
	// Add more than maxToolStatusLines entries
	tools := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, name := range tools {
		tr.OnStarted(name)
		tr.OnFinished(name, false)
	}
	out := tr.Format()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > maxToolStatusLines {
		t.Errorf("expected at most %d lines, got %d: %s", maxToolStatusLines, len(lines), out)
	}
	// Last entry should always be visible
	if !strings.Contains(out, "g") {
		t.Errorf("expected last tool 'g' to be visible, got %q", out)
	}
}

func TestToolStatusTracker_MultipleConcurrent(t *testing.T) {
	var tr ToolStatusTracker
	tr.OnStarted("fetch")
	tr.OnStarted("parse")
	out := tr.Format()
	if !strings.Contains(out, "⚙️ fetch…") {
		t.Errorf("expected fetch in-progress, got %q", out)
	}
	if !strings.Contains(out, "⚙️ parse…") {
		t.Errorf("expected parse in-progress, got %q", out)
	}
	tr.OnFinished("fetch", false)
	out = tr.Format()
	if !strings.Contains(out, "✅ fetch") {
		t.Errorf("expected fetch done, got %q", out)
	}
	if !strings.Contains(out, "⚙️ parse…") {
		t.Errorf("expected parse still in-progress, got %q", out)
	}
}

func TestToolStatusTracker_OnFinished_NoMatchingEntry(t *testing.T) {
	// OnFinished with no prior OnStarted should be a no-op (not panic).
	var tr ToolStatusTracker
	tr.OnFinished("ghost", false)
	if tr.HasAny() {
		t.Error("expected tracker to be empty after OnFinished with no prior OnStarted")
	}
}

func TestToolStatusTracker_SameName_MarksLastPending(t *testing.T) {
	// When the same tool is called twice, OnFinished should mark the last pending entry.
	var tr ToolStatusTracker
	tr.OnStarted("search")
	tr.OnFinished("search", false) // first call done
	tr.OnStarted("search")         // second call started
	out := tr.Format()
	// Should show one done and one in-progress.
	if !strings.Contains(out, "✅ search") {
		t.Errorf("expected first search to be marked done, got %q", out)
	}
	if !strings.Contains(out, "⚙️ search…") {
		t.Errorf("expected second search to be in-progress, got %q", out)
	}
}

// TestPlaceholderEditor_OnToolEvent_delegatesToTracker verifies that
// OnToolEvent correctly delegates to the embedded ToolStatusTracker.
// We use a nil bot/msg to avoid any real Telegram calls.
func TestPlaceholderEditor_OnToolEvent_delegatesToTracker(t *testing.T) {
	// Build an editor with nil bot/msg and zero rate-limit so the goroutine exits fast.
	editor := &PlaceholderEditor{minInterval: 0}

	if editor.HasAny() {
		t.Fatal("expected HasAny=false before any events")
	}

	// Simulate tool started.
	editor.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventStarted, ToolName: "search"})

	if !editor.HasAny() {
		t.Fatal("expected HasAny=true after ToolEventStarted")
	}
	if !strings.Contains(editor.tracker.Format(), "⚙️ search…") {
		t.Errorf("expected in-progress line, got %q", editor.tracker.Format())
	}

	// Simulate tool finished successfully.
	editor.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventFinished, ToolName: "search"})

	if !strings.Contains(editor.tracker.Format(), "✅ search") {
		t.Errorf("expected success line, got %q", editor.tracker.Format())
	}

	// Let the goroutine(s) spawned by schedule() drain.
	time.Sleep(10 * time.Millisecond)
}

// TestPlaceholderEditor_OnToolEvent_errorDelegation verifies that a failed
// tool event propagates the error flag through to the tracker.
func TestPlaceholderEditor_OnToolEvent_errorDelegation(t *testing.T) {
	editor := &PlaceholderEditor{minInterval: 0}

	editor.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventStarted, ToolName: "patch"})
	editor.OnToolEvent(agent.ToolEvent{
		Type:     agent.ToolEventFinished,
		ToolName: "patch",
		Err:      errors.New("write failed"),
	})

	if !strings.Contains(editor.tracker.Format(), "❌ patch") {
		t.Errorf("expected error line, got %q", editor.tracker.Format())
	}

	time.Sleep(10 * time.Millisecond)
}
