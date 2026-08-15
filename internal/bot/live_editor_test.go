package bot

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/tools"
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
	if !strings.Contains(out, "Working…") {
		t.Errorf("expected animated working placeholder, got %q", out)
	}
}

func TestLiveStreamEditor_ToolStarted(t *testing.T) {
	e := newTestLiveEditor()
	e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventStarted, ToolName: "search"})
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if !strings.Contains(out, "🔎 searching the web…") {
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
	if !strings.Contains(out, "⚙️ fetch ✓") {
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
	if !strings.Contains(out, "📝 editing files ✗") {
		t.Errorf("expected error status, got %q", out)
	}
}

func TestLiveStreamEditor_ToolFinishedDenial(t *testing.T) {
	e := newTestLiveEditor()
	denial := &tools.ToolDenial{
		ToolName: "web_fetch",
		Family:   "network",
		Reason:   "host \"evil.com\" is not in the network allowlist",
	}
	e.OnToolEvent(agent.ToolEvent{Type: agent.ToolEventStarted, ToolName: "web_fetch"})
	e.OnToolEvent(agent.ToolEvent{
		Type:     agent.ToolEventFinished,
		ToolName: "web_fetch",
		Err:      denial,
		Denial:   denial,
	})
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if !strings.Contains(out, "🌐 fetching a page 🚫") || !strings.Contains(out, "evil.com") {
		t.Errorf("expected denial status, got %q", out)
	}
}

func TestLiveStreamEditor_AppendDelta_NoTools(t *testing.T) {
	e := newTestLiveEditor()
	e.AppendDelta("Hello, ")
	e.AppendDelta("world!")
	e.mu.Lock()
	out := e.formatLocked()
	e.mu.Unlock()
	if !strings.HasSuffix(out, "Hello, world!") {
		t.Errorf("expected streamed content below the header, got %q", out)
	}
	if !strings.Contains(out, "Working…") {
		t.Errorf("expected animated header above the content, got %q", out)
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
	if !strings.Contains(out, "🔎 searching the web ✓") {
		t.Errorf("expected tool status in output, got %q", out)
	}
	if !strings.Contains(out, "Result: 42") {
		t.Errorf("expected content in output, got %q", out)
	}
	// Status should appear before content.
	statusIdx := strings.Index(out, "🔎 searching the web ✓")
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
	toolLineCount := strings.Count(out, "✓")
	if toolLineCount > maxToolStatusLines {
		t.Errorf("expected at most %d tool lines, got %d: %q", maxToolStatusLines, toolLineCount, out)
	}
	if !strings.Contains(out, "⚙️ g ✓") {
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

// TestLiveStreamEditor_ScheduleEdit_TokenThreshold verifies the burst condition
// directly. The old version raced with the background scheduler goroutine and
// was flaky even when the threshold logic was correct.
func TestLiveStreamEditor_ScheduleEdit_TokenThreshold(t *testing.T) {
	e := &LiveStreamEditor{}
	e.mu.Lock()
	e.pendingTokens = streamTokenThreshold
	e.lastEdit = time.Now()
	elapsed := time.Since(e.lastEdit)
	burst := e.pendingTokens >= streamTokenThreshold
	e.mu.Unlock()

	if !burst {
		t.Fatalf("expected pendingTokens >= %d", streamTokenThreshold)
	}
	if elapsed >= streamEditInterval {
		t.Fatalf("expected elapsed=%v to remain below streamEditInterval=%v for burst-only path", elapsed, streamEditInterval)
	}
}

func TestAckHandleAttachDetachEditor(t *testing.T) {
	h := &AckHandle{}
	if h.DetachEditor() != nil {
		t.Fatal("expected nil editor before attach")
	}
	e := &LiveStreamEditor{}
	h.AttachEditor(e)
	if got := h.DetachEditor(); got != e {
		t.Fatalf("DetachEditor = %p, want attached editor %p", got, e)
	}
	if h.DetachEditor() != nil {
		t.Fatal("expected nil editor after detach (consume-once)")
	}
}

func TestLiveStreamEditorStopAndDrainReturnsPromptly(t *testing.T) {
	e := &LiveStreamEditor{}
	done := make(chan struct{})
	go func() {
		e.StopAndDrain(2 * time.Second)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StopAndDrain did not return promptly on an idle editor")
	}
}

// TestLiveStreamEditorConstructorSpinnerStops guards the goroutine-leak class:
// a constructed editor's animate ticker must exit once Stop is called.
func TestLiveStreamEditorConstructorSpinnerStops(t *testing.T) {
	e := NewLiveStreamEditor(nil, nil)
	e.Stop()
	select {
	case <-e.stopCh:
		// closed — animate() exits on its next select iteration
	case <-time.After(time.Second):
		t.Fatal("stopCh not closed after Stop")
	}
	// Second Stop must be a no-op (no double-close panic).
	e.Stop()
}
