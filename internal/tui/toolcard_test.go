package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
)

// testModel creates a minimal Model for unit tests.
func testModel() *Model {
	ta := textarea.New()
	ta.Focus()
	vp := viewport.New(80, 20)
	return &Model{
		width:     80,
		height:    40,
		collapsed: make(map[int]bool),
		viewport:  vp,
		input:     ta,
		md:        newMDRenderer(78),
	}
}

func TestToolCardIndices(t *testing.T) {
	m := testModel()
	m.entries = []chatEntry{
		{role: "user", content: "hello"},
		{role: "tool", toolName: "read_file", toolArgs: "foo.go", toolRes: "contents"},
		{role: "assistant", content: "done"},
		{role: "tool", toolName: "grep", toolArgs: "bar", toolRes: "found"},
	}
	indices := m.toolCardIndices()
	if len(indices) != 2 {
		t.Fatalf("expected 2 tool indices, got %d", len(indices))
	}
	if indices[0] != 1 || indices[1] != 3 {
		t.Fatalf("expected indices [1,3], got %v", indices)
	}
}

func TestToolCardIndicesEmpty(t *testing.T) {
	m := testModel()
	m.entries = []chatEntry{
		{role: "user", content: "hello"},
		{role: "assistant", content: "hi"},
	}
	indices := m.toolCardIndices()
	if len(indices) != 0 {
		t.Fatalf("expected 0 tool indices, got %d", len(indices))
	}
}

func TestIsToolCardCollapsed_DefaultCollapsed(t *testing.T) {
	m := testModel()
	e := chatEntry{role: "tool", toolName: "read", toolRes: "ok"}
	if !m.isToolCardCollapsed(0, e) {
		t.Fatal("finished tool card should be collapsed by default")
	}
}

func TestIsToolCardCollapsed_InProgressNotCollapsed(t *testing.T) {
	m := testModel()
	e := chatEntry{role: "tool", toolName: "read"} // no result yet
	if m.isToolCardCollapsed(0, e) {
		t.Fatal("in-progress tool card should not be collapsed")
	}
}

func TestIsToolCardCollapsed_ExplicitExpand(t *testing.T) {
	m := testModel()
	e := chatEntry{role: "tool", toolName: "read", toolRes: "ok"}
	m.collapsed[0] = false // explicitly expanded
	if m.isToolCardCollapsed(0, e) {
		t.Fatal("explicitly expanded card should not be collapsed")
	}
}

func TestToolCardSummary(t *testing.T) {
	tests := []struct {
		name    string
		entry   chatEntry
		want    string
		wantLen bool // true if we just check non-empty
	}{
		{
			name:  "with result",
			entry: chatEntry{toolRes: "line1\nline2\nline3"},
			want:  "line1",
		},
		{
			name:  "with error",
			entry: chatEntry{toolErr: "something failed\ndetails"},
			want:  "something failed",
		},
		{
			name:  "no result or error",
			entry: chatEntry{},
			want:  "",
		},
		{
			name:  "long result truncated",
			entry: chatEntry{toolRes: strings.Repeat("x", 100)},
			want:  strings.Repeat("x", 60) + "…",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolCardSummary(tt.entry)
			if got != tt.want {
				t.Errorf("toolCardSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFocusedToolEntryIndex(t *testing.T) {
	m := testModel()
	m.entries = []chatEntry{
		{role: "user", content: "hello"},
		{role: "tool", toolName: "read", toolRes: "ok"},
		{role: "tool", toolName: "grep", toolRes: "found"},
	}

	// Not in nav mode
	if m.focusedToolEntryIndex() != -1 {
		t.Fatal("should be -1 when not in nav mode")
	}

	// In nav mode, cursor on first tool card
	m.toolCardNav = true
	m.toolCursor = 0
	if idx := m.focusedToolEntryIndex(); idx != 1 {
		t.Fatalf("expected focused index 1, got %d", idx)
	}

	// Cursor on second tool card
	m.toolCursor = 1
	if idx := m.focusedToolEntryIndex(); idx != 2 {
		t.Fatalf("expected focused index 2, got %d", idx)
	}

	// Out of range cursor
	m.toolCursor = 5
	if idx := m.focusedToolEntryIndex(); idx != -1 {
		t.Fatalf("expected -1 for out-of-range cursor, got %d", idx)
	}
}

func TestRenderToolCard_CollapsedContainsSummary(t *testing.T) {
	m := testModel()
	e := chatEntry{role: "tool", toolName: "read_file", toolRes: "file contents here"}
	m.entries = []chatEntry{e}

	rendered := m.renderToolCard(0, e)
	if !strings.Contains(rendered, "read_file") {
		t.Error("collapsed card should contain tool name")
	}
	if !strings.Contains(rendered, "✓") {
		t.Error("collapsed card should contain success indicator")
	}
	if !strings.Contains(rendered, "file contents here") {
		t.Error("collapsed card should contain summary")
	}
}

func TestRenderToolCard_CollapsedErrorIndicator(t *testing.T) {
	m := testModel()
	e := chatEntry{role: "tool", toolName: "exec", toolErr: "command failed"}
	m.entries = []chatEntry{e}

	rendered := m.renderToolCard(0, e)
	if !strings.Contains(rendered, "✗") {
		t.Error("collapsed error card should contain failure indicator")
	}
}

func TestRenderToolCard_ExpandedContainsArgs(t *testing.T) {
	m := testModel()
	e := chatEntry{role: "tool", toolName: "read_file", toolArgs: `{"path":"main.go"}`, toolRes: "package main"}
	m.entries = []chatEntry{e}
	m.collapsed[0] = false // explicitly expanded

	rendered := m.renderToolCard(0, e)
	if !strings.Contains(rendered, "args:") {
		t.Error("expanded card should contain args")
	}
	if !strings.Contains(rendered, "main.go") {
		t.Error("expanded card should show args content")
	}
}

func TestRenderToolCard_InProgressAlwaysExpanded(t *testing.T) {
	m := testModel()
	e := chatEntry{role: "tool", toolName: "long_task"}
	m.entries = []chatEntry{e}

	rendered := m.renderToolCard(0, e)
	if !strings.Contains(rendered, "running…") {
		t.Error("in-progress card should show 'running…'")
	}
}

func TestExitToolCardNav(t *testing.T) {
	m := testModel()
	m.toolCardNav = true
	m.input.Blur()

	m.exitToolCardNav()

	if m.toolCardNav {
		t.Error("toolCardNav should be false after exit")
	}
	if !m.input.Focused() {
		t.Error("input should be focused after exiting card nav")
	}
}
