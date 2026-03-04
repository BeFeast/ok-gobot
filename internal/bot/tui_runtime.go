package bot

import (
	"context"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/control"
)

// SubmitTUIRun submits an isolated TUI run through the bot's RuntimeHub.
// TUI runs intentionally use ChatID=0 so they stay independent from Telegram
// chat sessions while still reusing the same resolver, tools, and personality.
func (b *Bot) SubmitTUIRun(ctx context.Context, req control.TUIRunRequest) <-chan agent.RunEvent {
	if ctx == nil {
		ctx = context.Background()
	}

	var overrides *agent.RunOverrides
	if req.Model != "" {
		overrides = &agent.RunOverrides{Model: req.Model}
	}

	return b.hub.Submit(agent.RunRequest{
		SessionKey:   agent.SessionKey(req.SessionKey),
		ChatID:       0,
		Content:      req.Content,
		Session:      req.Session,
		History:      req.History,
		Context:      ctx,
		OnToolEvent:  req.OnToolEvent,
		OnDelta:      req.OnDelta,
		OnDeltaReset: req.OnDeltaReset,
		Overrides:    overrides,
	})
}

// AbortTUIRun cancels the active isolated TUI run for the provided session key.
func (b *Bot) AbortTUIRun(sessionKey string) {
	b.hub.Cancel(agent.SessionKey(sessionKey))
}
