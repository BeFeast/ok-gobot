package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/control"
	"ok-gobot/internal/logger"
)

// sessionKeyForChat returns the canonical session key for a Telegram chat.
// Private (DM) chats use "dm:<chatID>"; groups/supergroups/channels use "group:<chatID>".
func sessionKeyForChat(chat *telebot.Chat) agent.SessionKey {
	if chat.Type == telebot.ChatPrivate {
		return agent.NewDMSessionKey(chat.ID)
	}
	return agent.NewGroupSessionKey(chat.ID)
}

// processViaHub submits an inbound envelope to the legacy RuntimeHub compatibility
// path and renders the resulting events back to Telegram. New feature work should
// land on the chat/jobs runtime contract instead of expanding this flow.
func (b *Bot) processViaHub(ctx context.Context, delivery telegramDelivery, sessionKey agent.SessionKey, content, session string) error {
	return b.processViaHubWithContent(ctx, delivery, sessionKey, content, nil, session)
}

func (b *Bot) runViaHubAsync(
	ctx context.Context,
	delivery telegramDelivery,
	sessionKey agent.SessionKey,
	content string,
	userContent []ai.ContentBlock,
	session string,
	errorText string,
	runToken string,
) {
	chatID := delivery.Chat.ID

	go func() {
		defer func() {
			if runToken == "" {
				return
			}
			queued := b.queueManager.EndRun(chatID, runToken)
			if len(queued) == 0 {
				return
			}

			logger.Debugf("Bot: processing %d queued messages for chat=%d", len(queued), chatID)
			for _, qMsg := range queued {
				b.debouncer.Debounce(chatID, qMsg, func(qCombined string) {
					session, err := b.store.GetSession(chatID)
					if err != nil {
						log.Printf("Failed to get session for queued message: %v", err)
					}

					b.sendImmediateAck(delivery.Chat, 0)
					nextToken := b.queueManager.StartRun(chatID)
					b.runViaHubAsync(ctx, telegramDelivery{Chat: delivery.Chat}, sessionKey, qCombined, nil, session, errorText, nextToken)
				})
			}
		}()

		if err := b.processViaHubWithContent(ctx, delivery, sessionKey, content, userContent, session); err != nil {
			log.Printf("[bot] async hub run failed for session %s: %v", sessionKey, err)
			if errorText != "" {
				b.api.Send(delivery.Chat, errorText) //nolint:errcheck
			}
		}
	}()
}

func (b *Bot) processViaHubWithContent(
	ctx context.Context,
	delivery telegramDelivery,
	sessionKey agent.SessionKey,
	content string,
	userContent []ai.ContentBlock,
	session string,
) error {
	chatID := delivery.Chat.ID
	var jobID string

	// Set chat context so the LocalCommand ApprovalFunc can send prompts to the right chat.
	b.setCurrentChatID(chatID)
	defer b.setCurrentChatID(0)

	// Wire LiveStreamEditor for real-time token streaming and tool-event status lines.
	// The ⏳ ack message (sent upfront in the message handler) is continuously updated
	// while the run is active; processViaHub performs the authoritative final edit once
	// the run completes. Control hub events are also emitted for each tool lifecycle event.
	var liveEditor *LiveStreamEditor
	var onToolEvent func(agent.ToolEvent)
	var onDelta func(string)
	var onDeltaReset func()
	if ackHandle := b.ackManager.Peek(chatID); ackHandle != nil {
		jobID = ackHandle.JobID
		liveEditor = NewLiveStreamEditor(b.api, ackHandle.Message, ackHandle.JobID)
		liveEditor.Flush()
		ctrlHub := b.controlHub
		onToolEvent = func(event agent.ToolEvent) {
			liveEditor.OnToolEvent(event)
			if ctrlHub != nil {
				switch event.Type {
				case agent.ToolEventStarted:
					ctrlHub.Emit(control.EvtToolStarted, control.ToolEventPayload{
						ChatID:   chatID,
						ToolName: event.ToolName,
						Input:    event.Input,
					})
				case agent.ToolEventFinished:
					p := control.ToolEventPayload{ChatID: chatID, ToolName: event.ToolName, Output: event.Output}
					if event.Err != nil {
						p.Error = event.Err.Error()
					}
					ctrlHub.Emit(control.EvtToolFinished, p)
				}
			}
		}
		onDelta = func(delta string) {
			liveEditor.AppendDelta(delta)
			if ctrlHub != nil && delta != "" {
				ctrlHub.Emit(control.EvtRunDelta, control.RunDeltaPayload{
					ChatID: chatID,
					Delta:  delta,
				})
			}
		}
		onDeltaReset = func() {
			liveEditor.ResetContent()
		}
	} else if b.controlHub != nil {
		// No ack message, but we still want control hub events.
		ctrlHub := b.controlHub
		onToolEvent = func(event agent.ToolEvent) {
			switch event.Type {
			case agent.ToolEventStarted:
				ctrlHub.Emit(control.EvtToolStarted, control.ToolEventPayload{
					ChatID:   chatID,
					ToolName: event.ToolName,
					Input:    event.Input,
				})
			case agent.ToolEventFinished:
				p := control.ToolEventPayload{ChatID: chatID, ToolName: event.ToolName, Output: event.Output}
				if event.Err != nil {
					p.Error = event.Err.Error()
				}
				ctrlHub.Emit(control.EvtToolFinished, p)
			}
		}
		onDelta = func(delta string) {
			if delta == "" {
				return
			}
			ctrlHub.Emit(control.EvtRunDelta, control.RunDeltaPayload{
				ChatID: chatID,
				Delta:  delta,
			})
		}
	}

	// Emit session.accepted and run.started to control hub.
	if b.controlHub != nil {
		b.controlHub.Emit(control.EvtSessionAccepted, control.SessionInfo{
			ChatID: chatID,
			State:  "running",
		})
		b.controlHub.Emit(control.EvtRunStarted, control.RunEventPayload{ChatID: chatID})
	}

	// Start typing indicator while the hub is running.
	stopTyping := NewTypingIndicator(b.api, delivery.Chat)
	defer stopTyping()

	// Load multi-turn conversation history from the v2 transcript store.
	// Fetch a generous number of messages, then trim to fit the token budget
	// (40% of the model's context window). This adapts to message length and
	// model limits instead of using an arbitrary message count.
	model := b.getEffectiveModel(chatID)
	var history []ai.ChatMessage
	if summaryNodes, err := b.store.GetSessionSummaryNodes(string(sessionKey)); err == nil && len(summaryNodes) > 0 {
		roots := summaryRoots(summaryNodes)
		tail, tailErr := b.store.GetSessionMessagesV2AfterID(string(sessionKey), maxCoveredMessageID(roots), 500)
		if tailErr == nil {
			history = trimCompactedHistoryToTokenBudget(roots, tail, model)
		} else {
			log.Printf("[bot] failed to load tail after compacted roots for session %s: %v", sessionKey, tailErr)
		}
	}
	if len(history) == 0 {
		if v2Msgs, err := b.store.GetSessionMessagesV2(string(sessionKey), 500); err == nil && len(v2Msgs) > 0 {
			for _, m := range v2Msgs {
				history = append(history, ai.ChatMessage{Role: m.Role, Content: m.Content})
			}
		}
	}
	if len(history) > 0 {
		history = trimHistoryToTokenBudget(history, model)
	}

	// Submit to the hub — the hub owns agent resolution, tool execution,
	// and run lifecycle. We only provide the inbound envelope.
	req := agent.RunRequest{
		SessionKey:   sessionKey,
		ChatID:       chatID,
		Content:      content,
		UserContent:  userContent,
		Session:      session,
		History:      history,
		Context:      ctx,
		OnToolEvent:  onToolEvent,
		OnDelta:      onDelta,
		OnDeltaReset: onDeltaReset,
	}
	events := b.hub.Submit(req)

	// ── Render events back to Telegram ──

	var result *agent.AgentResponse
	var profileName string
	for ev := range events {
		switch ev.Type {
		case agent.RunEventDone:
			result = ev.Result
			profileName = ev.ProfileName

		case agent.RunEventError:
			stopTyping()
			if liveEditor != nil {
				liveEditor.Stop()
			}
			ackHandle := b.takeAckHandle(chatID)
			if ctx.Err() != nil || errors.Is(ev.Err, context.Canceled) {
				if ackHandle != nil {
					b.updateAckStatus(ackHandle, jobStatusCancelled, "Job stopped before completion.")
				}
				if b.controlHub != nil {
					b.controlHub.Emit(control.EvtRunFailed, control.RunEventPayload{
						ChatID: chatID,
						Error:  "cancelled",
					})
				}
				return nil
			}
			log.Printf("[bot] hub error for session %s: %v", sessionKey, ev.Err)
			if b.controlHub != nil {
				b.controlHub.Emit(control.EvtRunFailed, control.RunEventPayload{
					ChatID: chatID,
					Error:  ev.Err.Error(),
				})
			}
			errText := "❌ Sorry, I encountered an error processing your request."
			if ackHandle != nil {
				b.updateAckStatus(ackHandle, jobStatusFailed, "Sorry, I encountered an error processing your request.")
			} else {
				b.api.Send(delivery.Chat, errText) //nolint:errcheck
			}
			return nil
		}
	}

	if result == nil {
		// Run was cancelled before producing a result.
		if liveEditor != nil {
			liveEditor.Stop()
		}
		if ackHandle := b.takeAckHandle(chatID); ackHandle != nil {
			b.updateAckStatus(ackHandle, jobStatusCancelled, "Job stopped before completion.")
		}
		if b.controlHub != nil {
			b.controlHub.Emit(control.EvtRunFailed, control.RunEventPayload{
				ChatID: chatID,
				Error:  "cancelled",
			})
		}
		return nil
	}

	// Emit run.completed to control hub.
	if b.controlHub != nil {
		b.controlHub.Emit(control.EvtRunCompleted, control.RunEventPayload{ChatID: chatID})
	}

	// Record token usage.
	if result.PromptTokens > 0 || result.CompletionTokens > 0 {
		b.store.UpdateTokenUsage(chatID, result.PromptTokens, result.CompletionTokens, result.TotalTokens)
	}

	// Suppress internal sentinel tokens.
	trimmed := strings.TrimSpace(result.Message)
	if trimmed == "SILENT_REPLY" || trimmed == "HEARTBEAT_OK" {
		log.Printf("[bot] agent '%s' returned silent token: %s — suppressing reply", profileName, trimmed)
		if ackHandle := b.takeAckHandle(chatID); ackHandle != nil {
			b.updateAckStatus(ackHandle, jobStatusCompleted, "Completed with no direct reply.")
		}
		return nil
	}

	// Build the outbound message, optionally appending a usage footer.
	msg := result.Message
	usageMode, _ := b.store.GetSessionOption(chatID, "usage_mode")
	if (usageMode == "tokens" || usageMode == "full") && result.PromptTokens > 0 {
		msg += "\n\n" + FormatUsageFooter(result.PromptTokens, result.CompletionTokens)
	}

	// Extract and send emoji reactions.
	msg, reactions := parseReactions(msg)
	if len(reactions) > 0 && delivery.Message != nil {
		for _, emoji := range reactions {
			if err := b.api.React(delivery.Chat, delivery.Message, telebot.Reactions{
				Reactions: []telebot.Reaction{{Type: telebot.ReactionTypeEmoji, Emoji: emoji}},
			}); err != nil {
				log.Printf("[bot] failed to set reaction %s: %v", emoji, err)
			}
		}
	}

	// Extract reply-to tags.
	replyTarget := parseReplyTags(msg)
	msg = replyTarget.Clean

	// Guard against empty messages (Telegram rejects them).
	if strings.TrimSpace(msg) == "" {
		msg = "⚠️ Got an empty response from the model."
	}

	// Stop live streaming edits before the final authoritative edit so a
	// pending streaming goroutine does not overwrite the finalized content.
	if liveEditor != nil {
		liveEditor.Stop()
	}

	// Split long messages into chunks that fit Telegram's 4096-char limit.
	const maxTelegramLen = 4096
	chunks := splitMessage(msg, maxTelegramLen)

	// Mark the lifecycle placeholder as completed, then deliver the result asynchronously.
	if ackHandle := b.takeAckHandle(chatID); ackHandle != nil {
		b.updateAckStatus(ackHandle, jobStatusCompleted, "Result delivered below.")
	}
	for i, chunk := range chunks {
		sendOpts := &telebot.SendOptions{}
		if i == 0 {
			switch {
			case replyTarget.MessageID == -1:
				sendOpts.ReplyTo = delivery.Message
			case replyTarget.MessageID > 0:
				sendOpts.ReplyTo = &telebot.Message{ID: replyTarget.MessageID}
			}
		}
		if _, err := b.api.Send(delivery.Chat, chunk, sendOpts); err != nil {
			log.Printf("[bot] failed to send chunk %d for chat %d: %v", i, chatID, err)
		}
	}

	// Persist to daily memory.
	memoryEntry := fmt.Sprintf("Assistant (%s): %s", profileName, result.Message)
	if result.ToolUsed {
		memoryEntry += fmt.Sprintf(" [Tool: %s]", result.ToolName)
	}
	if err := b.memory.AppendToToday(memoryEntry); err != nil {
		log.Printf("[bot] failed to save to memory: %v", err)
	}

	// Persist session state unless the response is a synthetic fallback.
	// Fallback messages pollute history and cause the model to lose track of tasks.
	if !result.IsFallback {
		// Persist session state (legacy single-string for backwards compat).
		if err := b.store.SaveSession(chatID, result.Message); err != nil {
			log.Printf("[bot] failed to save session: %v", err)
		}
		// Persist both user and assistant messages to v2 transcript in a single
		// transaction on success only. A non-atomic write could leave an orphaned
		// user message that produces consecutive user turns on the next request,
		// which most providers (Anthropic, etc.) reject as invalid.
		if err := b.store.SaveSessionMessagePairV2(string(sessionKey), content, result.Message, jobID); err != nil {
			log.Printf("[bot] failed to persist v2 transcript: %v", err)
		}
	} else {
		log.Printf("[bot] skipping transcript persistence for synthetic fallback response")
	}

	log.Printf("[bot] session %s processed (agent: %s)", sessionKey, profileName)
	return nil
}

// trimHistoryToTokenBudget drops the oldest messages until the total fits within
// 40% of the model's context window. This is token-aware: long messages consume
// more budget, short messages leave room for more history. Messages are always
// dropped in pairs (user+assistant) to avoid orphaned roles.
func trimHistoryToTokenBudget(history []ai.ChatMessage, model string) []ai.ChatMessage {
	const historyBudgetFraction = 0.40
	budget := int(float64(agent.ModelLimits(model)) * historyBudgetFraction)
	return trimHistoryToBudget(history, budget)
}

func trimHistoryToBudget(history []ai.ChatMessage, budget int) []ai.ChatMessage {
	if len(history) == 0 || budget <= 0 {
		return history
	}

	total := countChatHistoryTokens(history)
	if total <= budget {
		return history
	}

	return trimChatHistoryFront(history, budget)
}

func trimChatHistoryFront(history []ai.ChatMessage, budget int) []ai.ChatMessage {
	if len(history) == 0 || budget <= 0 {
		return nil
	}

	tc := agent.NewTokenCounter()
	total := countChatHistoryTokens(history)

	// Drop messages from the front (oldest) until we fit.
	// Drop in pairs to keep user/assistant alternation clean.
	for len(history) > 0 && total > budget {
		dropCount := 1
		if len(history) > 1 {
			dropCount = 2
		}
		for i := 0; i < dropCount && len(history) > 0; i++ {
			dropped := tc.CountTokens(history[0].Content) + 4 // +4 for message overhead
			history = history[1:]
			total -= dropped
		}
	}

	return history
}

func countChatHistoryTokens(history []ai.ChatMessage) int {
	if len(history) == 0 {
		return 0
	}

	tc := agent.NewTokenCounter()
	msgs := make([]agent.Message, len(history))
	for i, m := range history {
		msgs[i] = agent.Message{Role: m.Role, Content: m.Content}
	}
	return tc.CountMessages(msgs)
}

// splitMessage breaks a long message into chunks that fit within maxLen.
// Splits on newline boundaries when possible.
func splitMessage(msg string, maxLen int) []string {
	if len(msg) <= maxLen {
		return []string{msg}
	}
	var chunks []string
	for len(msg) > 0 {
		if len(msg) <= maxLen {
			chunks = append(chunks, msg)
			break
		}
		// Find last newline before maxLen.
		cut := strings.LastIndex(msg[:maxLen], "\n")
		if cut <= 0 {
			cut = maxLen
		}
		chunks = append(chunks, msg[:cut])
		msg = msg[cut:]
		if len(msg) > 0 && msg[0] == '\n' {
			msg = msg[1:]
		}
	}
	return chunks
}
