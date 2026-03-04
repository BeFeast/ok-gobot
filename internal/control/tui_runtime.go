package control

import (
	"context"
	"time"

	"ok-gobot/internal/agent"
)

// TUIRunRequest describes one isolated TUI run routed through the bot runtime hub.
type TUIRunRequest struct {
	SessionKey   string
	Content      string
	Session      string
	Model        string
	OnToolEvent  func(agent.ToolEvent)
	OnDelta      func(string)
	OnDeltaReset func()
}

// TUIRunProvider is an optional state extension used by the control server TUI
// command path. Implementations submit isolated TUI runs to the bot runtime hub.
type TUIRunProvider interface {
	SubmitTUIRun(ctx context.Context, req TUIRunRequest) <-chan agent.RunEvent
	AbortTUIRun(sessionKey string)
}

type tuiSessionState struct {
	ID            string
	Name          string
	Model         string
	ModelOverride string
	LastAssistant string
	Running       bool
	CreatedAt     time.Time
}

type tuiSessionStore struct {
	byID   map[string]*tuiSessionState
	order  []string
	nextID int
}
