package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	controlserver "ok-gobot/internal/control"
)

// newInputHeightTestModel builds a minimal model for textarea auto-height tests.
func newInputHeightTestModel() *Model {
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
	m := newInputHeightTestModel()
	m.recalcInputHeight()
	if m.input.Height() != minInputLines {
		t.Errorf("expected height %d for empty input, got %d", minInputLines, m.input.Height())
	}
}

func TestRecalcInputHeight_MultipleLines(t *testing.T) {
	m := newInputHeightTestModel()
	m.input.SetValue("line1\nline2\nline3")
	m.recalcInputHeight()
	if m.input.Height() != 3 {
		t.Errorf("expected height 3 for 3-line input, got %d", m.input.Height())
	}
}

func TestRecalcInputHeight_CapsAtMax(t *testing.T) {
	m := newInputHeightTestModel()
	m.input.SetValue("1\n2\n3\n4\n5\n6\n7\n8")
	m.recalcInputHeight()
	if m.input.Height() != maxInputLines {
		t.Errorf("expected height capped at %d, got %d", maxInputLines, m.input.Height())
	}
}

func TestRecalcInputHeight_ShrinksAfterReset(t *testing.T) {
	m := newInputHeightTestModel()
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
	m := newInputHeightTestModel()
	m.input.SetHeight(3)
	got := m.inputAreaHeight()
	// 3 lines + 2 for top/bottom border
	if got != 5 {
		t.Errorf("expected inputAreaHeight 5, got %d", got)
	}
}

func newSplitLayoutTestModel() *Model {
	ta := textarea.New()
	ta.Focus()
	ta.SetHeight(3)
	ta.SetWidth(80)
	ta.CharLimit = 4096
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter")

	vp := viewport.New(80, 20)
	vp.SetContent("")

	return &Model{
		screen:           screenChat,
		streamIdx:        -1,
		viewport:         vp,
		input:            ta,
		paneFocus:        focusChat,
		sessionPaneWidth: 20,
		chatPaneWidth:    59,
		modelList:        defaultModelList,
	}
}

func TestTabTogglesPaneFocus(t *testing.T) {
	m := newSplitLayoutTestModel()
	m.width = 120
	m.height = 30
	m.resizeComponents()

	if !m.input.Focused() {
		t.Fatal("expected chat input focused initially")
	}

	m.handleChatKey(tea.KeyMsg{Type: tea.KeyTab}, nil)
	if m.paneFocus != focusSessions {
		t.Fatalf("expected focusSessions, got %v", m.paneFocus)
	}
	if m.input.Focused() {
		t.Fatal("expected input to be blurred when sessions pane is focused")
	}

	m.handleChatKey(tea.KeyMsg{Type: tea.KeyTab}, nil)
	if m.paneFocus != focusChat {
		t.Fatalf("expected focusChat, got %v", m.paneFocus)
	}
	if !m.input.Focused() {
		t.Fatal("expected input to be focused again after toggling back")
	}
}

func TestSessionPaneNavigationAndEnterSendsSwitch(t *testing.T) {
	m := newSplitLayoutTestModel()
	m.width = 100
	m.height = 24
	m.sessions = []controlserver.TUISessionInfo{
		{ID: "s1", Name: "default"},
		{ID: "s2", Name: "work"},
		{ID: "s3", Name: "coding"},
	}
	m.activeSession = "s1"
	m.resizeComponents()
	m.syncSessionSelection(true)
	m.setPaneFocus(focusSessions)

	m.handleChatKey(tea.KeyMsg{Type: tea.KeyDown}, nil)
	if m.sessionCursor != 1 {
		t.Fatalf("expected session cursor at 1, got %d", m.sessionCursor)
	}

	m.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}, nil)
	if m.lastErr != "send error: no active connection" {
		t.Fatalf("expected send to be attempted on Enter, got lastErr=%q", m.lastErr)
	}
}

func TestSessionPaneScrollsWithCursor(t *testing.T) {
	m := newSplitLayoutTestModel()
	m.width = 90
	m.height = 12
	for i := 0; i < 12; i++ {
		m.sessions = append(m.sessions, controlserver.TUISessionInfo{ID: "s" + strings.Repeat("x", i+1), Name: "session"})
	}
	m.activeSession = m.sessions[0].ID
	m.resizeComponents()
	m.syncSessionSelection(true)
	m.setPaneFocus(focusSessions)

	for i := 0; i < 8; i++ {
		m.handleChatKey(tea.KeyMsg{Type: tea.KeyDown}, nil)
	}

	if m.sessionCursor != 8 {
		t.Fatalf("expected cursor at 8, got %d", m.sessionCursor)
	}
	if m.sessionOffset == 0 {
		t.Fatal("expected non-zero sessionOffset after moving past visible rows")
	}
}

func TestViewRendersSplitWithSessionsAndChat(t *testing.T) {
	m := newSplitLayoutTestModel()
	m.width = 110
	m.height = 26
	m.sessions = []controlserver.TUISessionInfo{{ID: "s1", Name: "default"}, {ID: "s2", Name: "work"}}
	m.activeSession = "s1"
	m.entries = []chatEntry{{role: "user", content: "hi"}, {role: "assistant", content: "hello"}}
	m.resizeComponents()
	m.refreshViewport()

	view := m.View()
	for _, want := range []string{"Sessions", "You", "Bot", "│"} {
		if !strings.Contains(view, want) {
			t.Fatalf("expected view to contain %q", want)
		}
	}
}
