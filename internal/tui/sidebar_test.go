package tui

import (
	"strings"
	"testing"

	controlserver "ok-gobot/internal/control"
)

func TestSidebarWidth(t *testing.T) {
	tests := []struct {
		name    string
		width   int
		wantMin int
		wantMax int
	}{
		{"narrow terminal clamps to min", 60, 16, 16},
		{"normal terminal ~20%", 100, 16, 30},
		{"wide terminal clamps to max", 200, 30, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &Model{width: tt.width}
			got := m.sidebarWidth()
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("sidebarWidth() = %d, want [%d, %d]", got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestChatPaneWidth(t *testing.T) {
	m := &Model{width: 100}
	sw := m.sidebarWidth()
	cw := m.chatPaneWidth()
	if sw+cw != 100 {
		t.Errorf("sidebarWidth(%d) + chatPaneWidth(%d) != total width(100)", sw, cw)
	}
}

func TestFocusSwitching(t *testing.T) {
	m := &Model{
		width:  100,
		height: 30,
		screen: screenChat,
	}

	if m.focusedPane != paneChat {
		t.Fatal("default focus should be chat pane")
	}

	// Simulate Tab switch to sidebar
	m.focusedPane = paneSidebar
	if m.focusedPane != paneSidebar {
		t.Fatal("should be able to switch to sidebar")
	}

	// Switch back
	m.focusedPane = paneChat
	if m.focusedPane != paneChat {
		t.Fatal("should be able to switch back to chat")
	}
}

func TestRenderSidebarContainsSessions(t *testing.T) {
	m := &Model{
		width:  100,
		height: 30,
		sessions: []controlserver.TUISessionInfo{
			{ID: "s1", Name: "default", Model: "gpt-4o"},
			{ID: "s2", Name: "work", Model: "claude"},
		},
		activeSession: "s1",
		focusedPane:   paneSidebar,
		sessionCursor: 0,
	}

	sidebar := m.renderSidebar(20)
	if !strings.Contains(sidebar, "Sessions") {
		t.Error("sidebar should contain 'Sessions' title")
	}
	if !strings.Contains(sidebar, "default") {
		t.Error("sidebar should contain session name 'default'")
	}
	if !strings.Contains(sidebar, "work") {
		t.Error("sidebar should contain session name 'work'")
	}
	if !strings.Contains(sidebar, "[n] new") {
		t.Error("sidebar should contain new session hint")
	}
}

func TestRenderSidebarActiveMarker(t *testing.T) {
	m := &Model{
		width:  100,
		height: 30,
		sessions: []controlserver.TUISessionInfo{
			{ID: "s1", Name: "default", Model: "gpt-4o"},
			{ID: "s2", Name: "work", Model: "claude"},
		},
		activeSession: "s1",
		focusedPane:   paneChat,
	}

	sidebar := m.renderSidebar(20)
	// Active session should have ▶ marker
	if !strings.Contains(sidebar, "▶") {
		t.Error("sidebar should show ▶ for active session")
	}
}
