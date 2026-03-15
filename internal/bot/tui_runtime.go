package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/control"
	"ok-gobot/internal/jobs"
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

// SpawnSubagent is kept as a legacy control alias. The hard-reset runtime now
// turns this request into an explicit background job instead of an internal
// sub-agent run.
func (b *Bot) SpawnSubagent(parentChatID int64, task, agentName string) error {
	if b.jobService == nil {
		return fmt.Errorf("jobs service not initialized")
	}

	task = strings.TrimSpace(task)
	if task == "" {
		return fmt.Errorf("task is required")
	}

	profile := b.profileForJob(parentChatID, agentName)
	job, err := b.jobService.Launch(context.Background(), jobs.LaunchRequest{
		SessionKey:     string(controlJobSessionKey(parentChatID)),
		ChatID:         parentChatID,
		TaskType:       "control_job",
		Summary:        summarizeJobTask(task),
		RouterDecision: "subagent_alias",
		WorkerProfile:  profile.Name,
		Input: jobs.InputPayload{
			Prompt: task,
			Model:  b.profileModel(parentChatID, profile),
		},
	})
	if err != nil {
		return err
	}

	return b.SendMessage(parentChatID, fmt.Sprintf("⚙️ Job #%d queued", job.ID))
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

func controlJobSessionKey(chatID int64) agent.SessionKey {
	if chatID < 0 {
		return agent.NewGroupSessionKey(chatID)
	}
	return agent.NewDMSessionKey(chatID)
}

func (b *Bot) profileForJob(chatID int64, agentName string) *agent.AgentProfile {
	if strings.TrimSpace(agentName) != "" && b.agentRegistry != nil {
		if profile := b.agentRegistry.Get(agentName); profile != nil {
			return profile
		}
	}
	return b.activeProfile(chatID)
}

func summarizeJobTask(task string) string {
	summary := strings.Join(strings.Fields(task), " ")
	if len(summary) <= 120 {
		return summary
	}
	return summary[:117] + "..."
}
