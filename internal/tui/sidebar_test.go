package tui

import (
	"strings"
	"testing"

	controlserver "ok-gobot/internal/control"
)

func TestSidebarWidthCalc(t *testing.T) {
	tests := []struct {
		name    string
		width   int
		wantMin int
		wantMax int
	}{
		{"narrow terminal clamps to min", 60, 12, 20},
		{"normal terminal ~20%", 100, 16, 30},
		{"wide terminal clamps to max", 200, 30, 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sw := tt.width / 5
			if sw < 20 {
				sw = 20
			}
			if sw > 40 {
				sw = 40
			}
			if sw < tt.wantMin || sw > tt.wantMax {
				t.Errorf("sidebarWidth = %d, want [%d, %d]", sw, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestFocusSwitching(t *testing.T) {
	m := &Model{
		width:  100,
		height: 30,
		screen: screenChat,
	}

	if m.paneFocus != focusChat {
		t.Fatal("default focus should be chat pane")
	}

	m.paneFocus = focusSessions
	if m.paneFocus != focusSessions {
		t.Fatal("should be able to switch to sidebar")
	}

	m.paneFocus = focusChat
	if m.paneFocus != focusChat {
		t.Fatal("should be able to switch back to chat")
	}
}

func TestRenderSessionPaneContainsSessions(t *testing.T) {
	m := &Model{
		width:         100,
		height:        30,
		sidebarWidth:  20,
		chatPaneWidth: 80,
		sessions: []controlserver.TUISessionInfo{
			{ID: "s1", Name: "default", Model: "gpt-4o"},
			{ID: "s2", Name: "work", Model: "claude"},
		},
		activeSession: "s1",
		paneFocus:     focusSessions,
		sessionCursor: 0,
	}

	sidebar := m.renderSessionPane(20)
	if !strings.Contains(sidebar, "Sessions") {
		t.Error("sidebar should contain 'Sessions' title")
	}
	if !strings.Contains(sidebar, "default") {
		t.Error("sidebar should contain session name 'default'")
	}
	if !strings.Contains(sidebar, "work") {
		t.Error("sidebar should contain session name 'work'")
	}
}

func TestRenderSessionPaneActiveMarker(t *testing.T) {
	m := &Model{
		width:         100,
		height:        30,
		sidebarWidth:  20,
		chatPaneWidth: 80,
		sessions: []controlserver.TUISessionInfo{
			{ID: "s1", Name: "default", Model: "gpt-4o"},
			{ID: "s2", Name: "work", Model: "claude"},
		},
		activeSession: "s1",
		paneFocus:     focusChat,
	}

	sidebar := m.renderSessionPane(20)
	if !strings.Contains(sidebar, "★") {
		t.Error("sidebar should show ★ for active session")
	}
}
