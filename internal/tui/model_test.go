package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"

	controlserver "ok-gobot/internal/control"
)

// newTestModel builds a minimal Model suitable for unit tests (no WebSocket).
func newTestModel() *Model {
	ta := textarea.New()
	ta.SetHeight(1)
	ta.SetWidth(80)
	ta.CharLimit = 4096
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter")

	return &Model{
		width:     120,
		height:    40,
		input:     ta,
		streamIdx: -1,
	}
}

func TestRecalcInputHeight_EmptyInput(t *testing.T) {
	m := newTestModel()
	m.recalcInputHeight()
	if m.input.Height() != minInputLines {
		t.Errorf("expected height %d for empty input, got %d", minInputLines, m.input.Height())
	}
}

func TestRecalcInputHeight_MultipleLines(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("line1\nline2\nline3")
	m.recalcInputHeight()
	if m.input.Height() != 3 {
		t.Errorf("expected height 3 for 3-line input, got %d", m.input.Height())
	}
}

func TestRecalcInputHeight_CapsAtMax(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("1\n2\n3\n4\n5\n6\n7\n8")
	m.recalcInputHeight()
	if m.input.Height() != maxInputLines {
		t.Errorf("expected height capped at %d, got %d", maxInputLines, m.input.Height())
	}
}

func TestRecalcInputHeight_ShrinksAfterReset(t *testing.T) {
	m := newTestModel()
	m.input.SetValue("a\nb\nc\nd")
	m.recalcInputHeight()
	if m.input.Height() != 4 {
		t.Errorf("expected height 4 before reset, got %d", m.input.Height())
	}
	m.input.Reset()
	m.recalcInputHeight()
	if m.input.Height() != minInputLines {
		t.Errorf("expected height %d after reset, got %d", minInputLines, m.input.Height())
	}
}

func TestInputAreaHeight_IncludesBorder(t *testing.T) {
	m := newTestModel()
	m.input.SetHeight(3)
	got := m.inputAreaHeight()
	// 3 lines + 2 for top/bottom border
	if got != 5 {
		t.Errorf("expected inputAreaHeight 5, got %d", got)
	}
}

func TestHandleEventAssistantMessageMetadata(t *testing.T) {
	m := &Model{
		width:         120,
		viewport:      viewport.New(120, 20),
		streamIdx:     -1,
		activeSession: "main",
		sessions: []controlserver.TUISessionInfo{{
			ID:    "main",
			Name:  "Main",
			Model: "model-fallback",
		}},
	}

	msg := controlserver.ServerMsg{
		Kind:        controlserver.KindMessage,
		Role:        "assistant",
		Content:     "hello",
		Model:       "model-x",
		TotalTokens: 42,
		Timestamp:   "2026-03-05T09:07:00Z",
	}

	m.handleEvent(msg)

	if len(m.entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(m.entries))
	}
	entry := m.entries[0]
	if entry.role != "assistant" {
		t.Fatalf("expected assistant role, got %q", entry.role)
	}
	if entry.model != "model-x" {
		t.Fatalf("expected model metadata, got %q", entry.model)
	}
	if entry.tokens != 42 {
		t.Fatalf("expected token metadata 42, got %d", entry.tokens)
	}
	if got := entry.timestamp.Format("15:04"); got != "09:07" {
		t.Fatalf("expected timestamp 09:07, got %q", got)
	}
}

func TestRenderEntryAssistantShowsTimeAndMeta(t *testing.T) {
	m := &Model{width: 100}
	entry := chatEntry{
		role:      "assistant",
		content:   "test response",
		model:     "model-y",
		tokens:    73,
		timestamp: time.Date(2026, time.March, 5, 14, 22, 0, 0, time.UTC),
	}

	out := m.renderEntry(entry)

	if !strings.Contains(out, "14:22") {
		t.Fatalf("expected rendered timestamp in entry, output=%q", out)
	}
	if !strings.Contains(out, "model-y") || !strings.Contains(out, "73 tok") {
		t.Fatalf("expected rendered assistant metadata, output=%q", out)
	}
}

func TestRenderStatusShowsSessionModelAndState(t *testing.T) {
	m := &Model{
		width:         140,
		activeSession: "main",
		running:       true,
		sessions: []controlserver.TUISessionInfo{{
			ID:    "main",
			Name:  "Main Session",
			Model: "model-a",
		}},
	}

	out := m.renderStatus()
	for _, want := range []string{"session", "Main Session", "model", "model-a", "state", "running"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected status to contain %q, output=%q", want, out)
		}
	}

	m.running = false
	out = m.renderStatus()
	if !strings.Contains(out, "idle") {
		t.Fatalf("expected idle state in status, output=%q", out)
	}
}
