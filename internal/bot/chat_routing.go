package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	runtimepkg "ok-gobot/internal/runtime"
)

type taskNotificationStyle struct {
	doneHeading string
	failHeading string
}

var (
	taskCommandNotifications = taskNotificationStyle{
		doneHeading: "✅ *Task completed*",
		failHeading: "❌ *Task failed*",
	}
	backgroundJobNotifications = taskNotificationStyle{
		doneHeading: "✅ *Background job completed*",
		failHeading: "❌ *Background job failed*",
	}
)

func (b *Bot) handleCombinedChatTurn(
	ctx context.Context,
	c telebot.Context,
	sessionKey agent.SessionKey,
	content string,
	canReuseAck bool,
) error {
	decision := runtimepkg.DecideChatRoute(content)
	chatID := c.Chat().ID
	loggerReason := decision.Reason
	if loggerReason == "" {
		loggerReason = "unspecified"
	}
	log.Printf("[router] chat=%d action=%s reason=%s", chatID, decision.Action, loggerReason)

	switch decision.Action {
	case runtimepkg.ChatActionClarify:
		return b.respondWithClarification(c, sessionKey, content, decision.Clarification, canReuseAck)
	case runtimepkg.ChatActionLaunchJob:
		return b.launchBackgroundJob(c, content, canReuseAck)
	default:
		session, err := b.store.GetSession(chatID)
		if err != nil {
			log.Printf("Failed to get session: %v", err)
		}

		runToken := b.queueManager.StartRun(chatID)
		b.runViaHubAsync(ctx, newTelegramDelivery(c), sessionKey, content, nil, session,
			"❌ Sorry, I encountered an error processing your request.", runToken)
		return nil
	}
}

func (b *Bot) respondWithClarification(
	c telebot.Context,
	sessionKey agent.SessionKey,
	userContent string,
	clarification string,
	canReuseAck bool,
) error {
	if clarification == "" {
		clarification = "What should I work on exactly?"
	}
	if err := b.deliverRoutingText(c, clarification, canReuseAck); err != nil {
		return err
	}

	chatID := c.Chat().ID
	if err := b.store.SaveSession(chatID, clarification); err != nil {
		log.Printf("[router] failed to save clarification session for chat=%d: %v", chatID, err)
	}
	if err := b.store.SaveSessionMessagePairV2(string(sessionKey), userContent, clarification, ""); err != nil {
		log.Printf("[router] failed to persist clarification transcript for chat=%d: %v", chatID, err)
	}
	if err := b.memory.AppendToToday(fmt.Sprintf("Assistant (router): %s", clarification)); err != nil {
		log.Printf("[router] failed to append clarification to memory for chat=%d: %v", chatID, err)
	}
	return nil
}

func (b *Bot) launchBackgroundJob(c telebot.Context, task string, canReuseAck bool) error {
	ackText := fmt.Sprintf("⚙️ Background job started\nTask: %s", abbreviateForAck(task, 160))
	if err := b.deliverRoutingText(c, ackText, canReuseAck); err != nil {
		return err
	}

	b.startTaskRun(c.Chat(), c.Chat().ID, agent.SubagentSpawnRequest{Description: task}, backgroundJobNotifications)
	return nil
}

func (b *Bot) deliverRoutingText(c telebot.Context, text string, canReuseAck bool) error {
	if canReuseAck {
		if ackHandle := b.takeAckHandle(c.Chat().ID); ackHandle != nil {
			if _, err := b.api.Edit(ackHandle.Message, text); err == nil {
				return nil
			}
			_ = b.api.Delete(ackHandle.Message)
		}
	}
	return c.Send(text)
}

func (b *Bot) startTaskRun(chat *telebot.Chat, chatID int64, req agent.SubagentSpawnRequest, style taskNotificationStyle) {
	model := req.Model
	if model != "" {
		model = b.resolveModelAlias(model)
	}
	job := req.Job()
	if model != "" {
		job.Model = model
	}

	go func() {
		log.Printf("[task] spawning sub-agent for chat=%d model=%s thinking=%s desc=%.80s",
			chatID, model, req.ThinkLevel, req.Description)

		subKey := agent.SessionKey(fmt.Sprintf("subagent:%d:%d", chatID, time.Now().UnixNano()))
		contextModel := job.Model
		if contextModel == "" {
			contextModel = b.getEffectiveModel(chatID)
		}
		contextPack := b.buildJobContextPack(sessionKeyForChat(chat), req.Description, contextModel)

		events := b.hub.Submit(agent.RunRequest{
			SessionKey: subKey,
			ChatID:     chatID,
			Content:    req.Description,
			Session:    contextPack,
			Context:    context.Background(),
			Job:        &job,
			IsSubagent: true,
		})

		var notifText string
		for ev := range events {
			switch ev.Type {
			case agent.RunEventDone:
				result := ""
				if ev.Result != nil {
					result = ev.Result.Message
				}
				notifText = fmt.Sprintf("%s\n\n%s", style.doneHeading, job.CompletionSummary(result))
			case agent.RunEventError:
				notifText = fmt.Sprintf("%s\n\n%s", style.failHeading, ev.Err.Error())
			}
		}

		if notifText != "" {
			if _, err := b.api.Send(chat, notifText, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
				log.Printf("[task] failed to send completion notification to chat=%d: %v", chatID, err)
			}
		}
	}()
}

func abbreviateForAck(input string, maxRunes int) string {
	compact := strings.Join(strings.Fields(strings.TrimSpace(input)), " ")
	if compact == "" {
		return ""
	}

	runes := []rune(compact)
	if len(runes) <= maxRunes {
		return compact
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}
