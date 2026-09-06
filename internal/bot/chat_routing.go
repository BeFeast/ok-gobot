package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	runtimepkg "ok-gobot/internal/runtime"
)

type taskNotificationStyle struct {
	doneHeading string
	failHeading string
}

var taskCommandNotifications = taskNotificationStyle{
	doneHeading: "✅ *Task completed*",
	failHeading: "❌ *Task failed*",
}

// handleCombinedChatTurn executes an inbound chat turn as an agent turn.
//
// There is no pre-classification: the model receives every message and decides
// for itself whether the request needs tools. A rules-first keyword router used
// to sit here and split turns into reply / clarification / background job, but
// it scored an English-only lexicon, so this bot's Russian traffic could never
// be recognised as work. Explicit background work is still available through
// the /task command.
func (b *Bot) handleCombinedChatTurn(
	ctx context.Context,
	c telebot.Context,
	sessionKey agent.SessionKey,
	content string,
) error {
	chatID := c.Chat().ID

	session, err := b.store.GetSession(chatID)
	if err != nil {
		log.Printf("Failed to get session: %v", err)
	}

	runToken := b.queueManager.StartRun(chatID)
	// No RunOverrides: chat turns resolve to the session/profile/default model,
	// exactly like the media and /steer paths. The interaction fast lane used to
	// be flagged here for turns the router had classified as light; with the
	// classifier gone there is nothing left to justify pinning a cheaper model
	// to a request the model has not read yet.
	b.runViaHubAsync(ctx, b.newTesseraDelivery(c), sessionKey, content, nil, session,
		nil, runFailureText, runToken)
	return nil
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

func (b *Bot) startTaskRun(chat *telebot.Chat, chatID int64, req agent.SubagentSpawnRequest, style taskNotificationStyle, placeholder *telebot.Message) {
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

		// Heartbeat: refresh the placeholder with elapsed time so a long run
		// is distinguishable from a hang (frozen "working on it" reads as dead).
		var beatWG sync.WaitGroup
		stopBeat := make(chan struct{})
		if placeholder != nil {
			beatWG.Add(1)
			go func() {
				defer beatWG.Done()
				start := time.Now()
				ticker := time.NewTicker(25 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-stopBeat:
						return
					case <-ticker.C:
						elapsed := time.Since(start).Round(time.Second)
						txt := fmt.Sprintf("🚀 Working on it in the background (⏱ %s)…\nTask: %s", elapsed, abbreviateForAck(req.Description, 160))
						if _, err := b.api.Edit(placeholder, txt); err != nil {
							return // message deleted or chat gone — stop beating
						}
					}
				}
			}()
		}

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

		close(stopBeat)
		beatWG.Wait()

		if notifText != "" {
			// Commit before sending. Until this row exists the answer lives only
			// in this goroutine, and a failed send or a restart loses it for good
			// (four such losses in the 2026-08 journal). Ordering is the design.
			outboxID, enqueueErr := b.store.EnqueueOutboxSending(chatID, notifText, "task")
			if enqueueErr != nil {
				log.Printf("[task] could not persist completion for chat=%d: %v", chatID, enqueueErr)
			}

			delivered := false
			var sentID int64
			var lastErr error
			if placeholder != nil {
				// Replace the stale "working on it" bubble with the outcome.
				if msg, err := editMarkdownWithPlainFallback(b.api, placeholder, notifText); err == nil {
					delivered = true
					if msg != nil {
						sentID = int64(msg.ID)
					}
				} else {
					lastErr = err
				}
			}
			if !delivered {
				if msg, err := sendMarkdownWithPlainFallback(b.api, chat, notifText); err != nil {
					lastErr = err
					log.Printf("[task] failed to send completion notification to chat=%d: %v", chatID, err)
				} else {
					delivered = true
					if msg != nil {
						sentID = int64(msg.ID)
					}
				}
			}

			if enqueueErr == nil {
				if delivered {
					if err := b.store.MarkOutboxDelivered(outboxID, sentID); err != nil {
						log.Printf("[task] could not mark completion delivered id=%d: %v", outboxID, err)
					}
				} else {
					reason := "unknown send failure"
					if lastErr != nil {
						reason = lastErr.Error()
					}
					if err := b.store.RecordOutboxFailure(outboxID, reason); err != nil {
						log.Printf("[task] could not record delivery failure id=%d: %v", outboxID, err)
					}
				}
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
