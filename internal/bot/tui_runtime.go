package bot

import (
	"context"
	"fmt"
	"log"
	"time"

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
		UserContent:  req.UserContent,
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

// LogTUIExchange writes a TUI conversation turn to the session store (chatID=-1).
// Intentionally does NOT write to daily memory to avoid polluting the context window.
func (b *Bot) LogTUIExchange(userText, assistantText string) {
	if assistantText != "" {
		if err := b.store.SaveSession(-1, assistantText); err != nil {
			log.Printf("[tui] failed to save tui session: %v", err)
		}
	}
}

// GetStatusText returns a formatted status string for the TUI /status command.
// Reuses the same logic as the Telegram /status handler.
func (b *Bot) GetStatusText(sessionID string) string {
	return b.buildStatusString(-1)
}

// RunCronTask processes a cron job's task description through the agent.
// The result is sent to the job's associated chat.
func (b *Bot) RunCronTask(ctx context.Context, chatID int64, task string) error {
	subKey := agent.SessionKey(fmt.Sprintf("cron:%d:%d", chatID, time.Now().UnixNano()))

	events := b.hub.Submit(agent.RunRequest{
		SessionKey: subKey,
		ChatID:     chatID,
		Content:    task,
		Context:    ctx,
	})

	for ev := range events {
		switch ev.Type {
		case agent.RunEventDone:
			msg := ev.Result.Message
			if msg != "" {
				b.SendMessage(chatID, msg) //nolint:errcheck
			}
		case agent.RunEventError:
			return ev.Err
		}
	}
	return nil
}
