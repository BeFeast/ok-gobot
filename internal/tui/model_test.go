package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
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
		screen:    screenChat,
		input:     ta,
		streamIdx: -1,
	}
}

func newCompletionTestModel() Model {
	ta := textarea.New()
	ta.SetWidth(80)
	ta.SetHeight(3)

	return Model{
		screen: screenChat,
		input:  ta,
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

func TestRefreshCompletionsShowsAllForSlash(t *testing.T) {
	m := newCompletionTestModel()
	m.input.SetValue("/")

	m.refreshCompletions()

	if !m.completionVisible {
		t.Fatalf("expected completion popup to be visible")
	}
	if got, want := len(m.completionItems), len(commandCompletions); got != want {
		t.Fatalf("expected %d completion items, got %d", want, got)
	}
}

func TestRefreshCompletionsFiltersByPrefix(t *testing.T) {
	m := newCompletionTestModel()
	m.input.SetValue("/mo")

	m.refreshCompletions()

	if !m.completionVisible {
		t.Fatalf("expected completion popup to be visible")
	}
	if got, want := len(m.completionItems), 1; got != want {
		t.Fatalf("expected %d completion item, got %d", want, got)
	}
	if got, want := m.completionItems[0].name, "/model"; got != want {
		t.Fatalf("expected first completion to be %q, got %q", want, got)
	}
}

func TestEscDismissesCompletionPopup(t *testing.T) {
	m := newCompletionTestModel()
	m.input.SetValue("/st")
	m.refreshCompletions()

	_, _ = m.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc}, nil)

	if m.completionVisible {
		t.Fatalf("expected completion popup to be hidden after esc")
	}
	if got, want := m.input.Value(), "/st"; got != want {
		t.Fatalf("expected input to stay %q, got %q", want, got)
	}

	m.refreshCompletions()
	if m.completionVisible {
		t.Fatalf("expected completion popup to remain hidden until input changes")
	}

	m.input.SetValue("/sta")
	m.refreshCompletions()
	if !m.completionVisible {
		t.Fatalf("expected completion popup to reopen after input change")
	}
}

func TestEnterCompletesSelectedCommand(t *testing.T) {
	m := newCompletionTestModel()
	m.input.SetValue("/st")
	m.refreshCompletions()

	_, _ = m.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}, nil)

	if got, want := m.input.Value(), "/status "; got != want {
		t.Fatalf("expected input to be %q, got %q", want, got)
	}
	if m.completionVisible {
		t.Fatalf("expected completion popup to be hidden after completion")
	}
}

func TestSlashCommandPrefix(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		okWant bool
	}{
		{name: "slash only", input: "/", want: "", okWant: true},
		{name: "typed prefix", input: "/co", want: "co", okWant: true},
		{name: "uppercase normalised", input: "/TH", want: "th", okWant: true},
		{name: "command with args disabled", input: "/new test", okWant: false},
		{name: "plain text", input: "hello", okWant: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := slashCommandPrefix(tc.input)
			if ok != tc.okWant {
				t.Fatalf("expected ok=%v, got %v", tc.okWant, ok)
			}
			if got != tc.want {
				t.Fatalf("expected prefix %q, got %q", tc.want, got)
			}
		})
	}
}
