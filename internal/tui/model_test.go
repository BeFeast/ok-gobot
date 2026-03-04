package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
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
