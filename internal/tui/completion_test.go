package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
)

func newTestChatModel() *Model {
	ta := textarea.New()
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.Focus()
	return &Model{
		screen: screenChat,
		input:  ta,
		width:  120,
		height: 40,
	}
}

func TestParseCompletionQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "slash only", input: "/", want: "", ok: true},
		{name: "command prefix", input: "/st", want: "st", ok: true},
		{name: "ignores leading spaces", input: "   /mo", want: "mo", ok: true},
		{name: "not a command", input: "hello", want: "", ok: false},
		{name: "has args", input: "/status now", want: "", ok: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseCompletionQuery(tc.input)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("parseCompletionQuery(%q) = (%q, %v), want (%q, %v)", tc.input, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestFilterCommandCompletions(t *testing.T) {
	t.Parallel()

	all := filterCommandCompletions("")
	if len(all) != len(commandCompletions) {
		t.Fatalf("expected %d commands, got %d", len(commandCompletions), len(all))
	}

	st := filterCommandCompletions("st")
	if len(st) != 2 {
		t.Fatalf("expected 2 /st matches, got %d", len(st))
	}
	if st[0].name != "/status" || st[1].name != "/stop" {
		t.Fatalf("unexpected /st matches: %+v", st)
	}

	none := filterCommandCompletions("does-not-exist")
	if len(none) != 0 {
		t.Fatalf("expected no matches, got %d", len(none))
	}
}

func TestUpdateCompletionVisibility(t *testing.T) {
	t.Parallel()

	m := newTestChatModel()

	m.input.SetValue("/")
	m.updateCompletion()
	if !m.completion.visible {
		t.Fatal("expected completion to be visible for /")
	}
	if len(m.completion.items) != len(commandCompletions) {
		t.Fatalf("expected %d items, got %d", len(commandCompletions), len(m.completion.items))
	}

	m.input.SetValue("/status now")
	m.updateCompletion()
	if m.completion.visible {
		t.Fatal("expected completion to hide when args are present")
	}

	m.input.SetValue("plain text")
	m.updateCompletion()
	if m.completion.visible {
		t.Fatal("expected completion to hide for non-command input")
	}
}

func TestHandleChatKeyCompletionNavigationAndTabApply(t *testing.T) {
	t.Parallel()

	m := newTestChatModel()
	m.input.SetValue("/")
	m.updateCompletion()

	if m.completion.selected != 0 {
		t.Fatalf("expected initial selection 0, got %d", m.completion.selected)
	}

	m.handleChatKey(tea.KeyMsg{Type: tea.KeyDown}, nil)
	if m.completion.selected != 1 {
		t.Fatalf("expected selection 1 after down, got %d", m.completion.selected)
	}

	m.handleChatKey(tea.KeyMsg{Type: tea.KeyTab}, nil)
	if m.completion.visible {
		t.Fatal("expected completion popup to be hidden after apply")
	}
	if m.input.Value() != "/usage " {
		t.Fatalf("expected tab apply to set /usage, got %q", m.input.Value())
	}
}

func TestHandleChatKeyEscDismissesCompletion(t *testing.T) {
	t.Parallel()

	m := newTestChatModel()
	m.input.SetValue("/")
	m.updateCompletion()

	m.handleChatKey(tea.KeyMsg{Type: tea.KeyEsc}, nil)
	if m.completion.visible {
		t.Fatal("expected esc to dismiss completion")
	}
	if m.input.Value() != "/" {
		t.Fatalf("expected input to remain unchanged, got %q", m.input.Value())
	}
}

func TestHandleChatKeyEnterCompletesCommand(t *testing.T) {
	t.Parallel()

	m := newTestChatModel()
	m.input.SetValue("/st")
	m.updateCompletion()

	m.handleChatKey(tea.KeyMsg{Type: tea.KeyEnter}, nil)
	if m.completion.visible {
		t.Fatal("expected enter to apply completion and hide popup")
	}
	if m.input.Value() != "/status " {
		t.Fatalf("expected enter to complete /status, got %q", m.input.Value())
	}
}
