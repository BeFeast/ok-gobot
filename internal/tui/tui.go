// Package tui provides the terminal UI for ok-gobot built with Bubble Tea.
// It connects to the local control server over WebSocket and provides
// a first-class interactive surface alongside Telegram.
package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	controlserver "ok-gobot/internal/control"
)

// defaultModelList is the set of models shown in the model picker.
var defaultModelList = []string{
	"moonshotai/kimi-k2.5",
	"anthropic/claude-opus-4-5-20251101",
	"anthropic/claude-sonnet-4-5-20250929",
	"anthropic/claude-haiku-3-5-20241022",
	"openai/gpt-4o",
	"openai/gpt-4o-mini",
	"google/gemini-2.5-pro",
	"google/gemini-2.5-flash",
	"deepseek/deepseek-chat-v3-0324",
}

// Options configures the TUI startup.
type Options struct {
	// ServerAddr is the address of the control server (e.g. "127.0.0.1:8787").
	ServerAddr string
	// ModelList overrides the built-in model picker list.
	ModelList []string
}

// Run starts the Bubble Tea TUI and blocks until the user quits.
func Run(opts Options) error {
	conn, err := dialWS(opts.ServerAddr)
	if err != nil {
		return fmt.Errorf("Could not connect to ok-gobot server at %s — is it running?", opts.ServerAddr)
	}

	modelList := opts.ModelList
	if len(modelList) == 0 {
		modelList = defaultModelList
	}

	m := newModel(conn, opts.ServerAddr, modelList)

	p := tea.NewProgram(m,
		tea.WithAltScreen(),
	)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	return nil
}

// newModel creates the initial Model.
func newModel(conn *wsConn, addr string, models []string) *Model {
	// Textarea for input — starts at 1 line and auto-expands up to maxInputLines.
	ta := textarea.New()
	ta.Placeholder = "Type a message… (Enter to send, Shift+Enter for newline)"
	ta.Focus()
	ta.SetHeight(1)
	ta.SetWidth(80)
	ta.CharLimit = 4096
	ta.ShowLineNumbers = false

	// Shift+Enter (or Alt+Enter) inserts a newline; bare Enter sends.
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter")

	// Viewport for chat log
	vp := viewport.New(80, 20)
	vp.SetContent("")

	// Seed the listenCmd loop - get the first server msg to discover sessions
	// We send a list_sessions command right away
	_ = conn.send(controlserver.ClientMsg{Type: controlserver.CmdListSessions})

	return &Model{
		conn:       conn,
		serverAddr: addr,
		streamIdx:  -1,
		collapsed:  make(map[int]bool),
		viewport:   vp,
		input:      ta,
		modelList:  models,
	}
}
