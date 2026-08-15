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
		// The interaction fast lane is flagged ONLY here: plain text replies
		// classified by the rules router. Media, /steer, jobs, and custom
		// agent flows keep their default model. The resolver owns the policy.
		b.runViaHubAsync(ctx, newTelegramDelivery(c), sessionKey, content, nil, session,
			interactionLane(),
			runFailureText, runToken)
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
	b.appendToTelegramMemory(c.Chat(), senderIDFromMessage(c.Message()), fmt.Sprintf("Assistant (router): %s", clarification))
	return nil
}

func (b *Bot) launchBackgroundJob(c telebot.Context, task string, canReuseAck bool) error {
	ackText := backgroundJobAck(abbreviateForAck(task, 160))
	if err := b.deliverRoutingText(c, ackText, canReuseAck); err != nil {
		return err
	}

	// Router-classified heavy work runs on the premium tier: capable model
	// plus the tier's tool-call/duration budgets.
	b.startTaskRun(c.Chat(), c.Chat().ID, agent.SubagentSpawnRequest{Description: task, Tier: "premium"}, backgroundJobNotifications)
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

// resolveJobTier resolves an explicit tier request for a delegated chat job.
// Empty or unknown requests run untiered (logged, never coerced).
func (b *Bot) resolveJobTier(tier string) (string, runtimepkg.TierConfig, bool) {
	tier = strings.TrimSpace(tier)
	if b.workerSelector == nil || tier == "" {
		return "", runtimepkg.TierConfig{}, false
	}
	requested, ok := runtimepkg.ParseCostTier(tier)
	if !ok {
		log.Printf("[task] unknown cost tier %q, running untiered", tier)
		return "", runtimepkg.TierConfig{}, false
	}
	resolved, tierCfg, err := b.workerSelector.Resolve("", requested)
	if err != nil {
		log.Printf("[task] cost tier %q unresolved, running untiered: %v", requested, err)
		return "", runtimepkg.TierConfig{}, false
	}
	return string(resolved), tierCfg, true
}

func (b *Bot) startTaskRun(chat *telebot.Chat, chatID int64, req agent.SubagentSpawnRequest, style taskNotificationStyle) {
	model := req.Model
	if model != "" {
		model = b.resolveModelAlias(model)
	}
	// Tiers are strictly opt-in and strictly soft: budgets fill only zero
	// fields of the raw request (before WithDefaults), while the tier's
	// model/thinking travel as RunOverrides.TierModel/TierThinking so the
	// resolver keeps them UNDER session /model and /think. Explicit /task
	// flags ride RunOverrides.Model/ThinkLevel and win over everything.
	raw := req.RawJob()
	raw.Model = model
	tierLabel, tierCfg, tiered := b.resolveJobTier(req.Tier)
	if tiered {
		budgets := tierCfg
		budgets.Model, budgets.Thinking = "", ""
		raw = runtimepkg.FillDelegation(raw, budgets)
	}
	job := raw.WithDefaults()

	overrides := &agent.RunOverrides{
		Model:        model,
		ThinkLevel:   req.ThinkLevel,
		TierModel:    tierCfg.Model,
		TierThinking: tierCfg.Thinking,
	}

	go func() {
		log.Printf("[task] spawning sub-agent for chat=%d model=%s thinking=%s tier=%s desc=%.80s",
			chatID, model, req.ThinkLevel, tierLabel, req.Description)

		subKey := agent.SessionKey(fmt.Sprintf("subagent:%d:%d", chatID, time.Now().UnixNano()))

		events := b.hub.Submit(agent.RunRequest{
			SessionKey:  subKey,
			ChatID:      chatID,
			Content:     req.Description,
			Session:     "",
			Context:     context.Background(),
			Overrides:   overrides,
			Job:         &job,
			IsSubagent:  true,
			MemoryScope: b.memoryRecallContext(chatID, 0, string(chat.Type), subKey),
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
