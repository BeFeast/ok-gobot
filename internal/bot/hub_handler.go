package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/control"
	"ok-gobot/internal/logger"
	"ok-gobot/internal/memory"
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
	return b.processViaHubWithContent(ctx, delivery, sessionKey, content, nil, session, nil)
}

func (b *Bot) runViaHubAsync(
	ctx context.Context,
	delivery telegramDelivery,
	sessionKey agent.SessionKey,
	content string,
	userContent []ai.ContentBlock,
	session string,
	overrides *agent.RunOverrides,
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
					// Queued drains are ordinary agent turns, like the live path.
					b.runViaHubAsync(ctx, telegramDelivery{Chat: delivery.Chat}, sessionKey, qCombined, nil, session, nil, errorText, nextToken)
				})
			}
		}()

		if err := b.processViaHubWithContent(ctx, delivery, sessionKey, content, userContent, session, overrides); err != nil {
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
	overrides *agent.RunOverrides,
) error {
	chatID := delivery.Chat.ID
	var jobID string

	// Set chat context so the LocalCommand ApprovalFunc can send prompts to the right chat.
	b.setCurrentChatID(chatID)
	defer b.setCurrentChatID(0)

	// Track which skills are read during this run so scores can be updated on completion.
	tracker := newSkillTracker()

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
		liveEditor = NewLiveStreamEditor(b.api, ackHandle.Message)
		// Attach so terminal status writers (updateAckStatus, including the
		// queue-interrupt path) stop the editor before their final edit, and
		// defer Stop so no return path can leak the spinner ticker.
		ackHandle.AttachEditor(liveEditor)
		defer liveEditor.Stop()
		liveEditor.Flush()
		ctrlHub := b.controlHub
		onToolEvent = func(event agent.ToolEvent) {
			tracker.observe(event)
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
					if event.Denial != nil {
						ctrlHub.Emit(control.EvtToolDenied, control.ToolDeniedPayload{
							ChatID:      chatID,
							ToolName:    event.Denial.ToolName,
							Family:      event.Denial.Family,
							Reason:      event.Denial.Reason,
							Remediation: event.Denial.Remediation,
						})
					} else {
						p := control.ToolEventPayload{ChatID: chatID, ToolName: event.ToolName, Output: event.Output}
						if event.Err != nil {
							p.Error = event.Err.Error()
						}
						ctrlHub.Emit(control.EvtToolFinished, p)
					}
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
			tracker.observe(event)
			switch event.Type {
			case agent.ToolEventStarted:
				ctrlHub.Emit(control.EvtToolStarted, control.ToolEventPayload{
					ChatID:   chatID,
					ToolName: event.ToolName,
					Input:    event.Input,
				})
			case agent.ToolEventFinished:
				if event.Denial != nil {
					ctrlHub.Emit(control.EvtToolDenied, control.ToolDeniedPayload{
						ChatID:      chatID,
						ToolName:    event.Denial.ToolName,
						Family:      event.Denial.Family,
						Reason:      event.Denial.Reason,
						Remediation: event.Denial.Remediation,
					})
				} else {
					p := control.ToolEventPayload{ChatID: chatID, ToolName: event.ToolName, Output: event.Output}
					if event.Err != nil {
						p.Error = event.Err.Error()
					}
					ctrlHub.Emit(control.EvtToolFinished, p)
				}
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

	// Ensure skill tracker observes events even when no other listeners are wired.
	if onToolEvent == nil {
		onToolEvent = func(event agent.ToolEvent) {
			tracker.observe(event)
		}
	}

	// Start typing indicator while the hub is running.
	stopTyping := NewTypingIndicator(b.api, delivery.Chat)
	defer stopTyping()

	// Load multi-turn conversation history from the v2 transcript store.
	// When compaction summaries exist, search both summaries and raw
	// transcript together and expand only the relevant branch instead of
	// replaying the full history. Falls back to simple token-budget
	// trimming when no compactions are present.
	var history []ai.ChatMessage
	if v2Msgs, err := b.store.GetSessionMessagesV2(string(sessionKey), 500); err == nil && len(v2Msgs) > 0 {
		for _, m := range v2Msgs {
			history = append(history, ai.ChatMessage{Role: m.Role, Content: m.Content})
		}
		history = buildRunHistory(history, content, b.getEffectiveModel(chatID))
	}

	// Run the bounded pre-reply Active Memory recall step. The result is
	// always non-nil; on disabled/skipped/timeout/no-results we fall through
	// to the main run with no injected context. Errors never block the reply.
	userID := senderIDFromMessage(delivery.Message)
	preNotes, activeMemDiag := b.runActiveMemoryRecall(ctx, sessionKey, chatID, userID, content, history)
	if activeMemDiag != "" {
		b.maybeSendVerboseDiagnostic(chatID, delivery.Chat, activeMemDiag)
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
		Overrides:    overrides,
		OnToolEvent:  onToolEvent,
		OnDelta:      onDelta,
		OnDeltaReset: onDeltaReset,
		OnRunStarted: func(info agent.RunStartInfo) {
			if b.controlHub == nil {
				return
			}
			b.controlHub.Emit(control.EvtSessionAccepted, control.SessionInfo{
				ChatID:        info.ChatID,
				Model:         info.Model,
				ModelTier:     valueOrStatus(info.ModelTier, "default"),
				Effort:        valueOrStatus(info.Effort, "off"),
				Backend:       info.Backend,
				BackendHealth: string(info.BackendHealth.Status),
				State:         "running",
			})
			b.controlHub.Emit(control.EvtRunStarted, control.RunEventPayload{ChatID: info.ChatID})
		},
		PreUserSystemNotes: preNotes,
		MemoryScope:        b.memoryRecallContext(chatID, userID, string(delivery.Chat.Type), sessionKey),
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
			tracker.flush(b.store, false)
			stopTyping()
			if liveEditor != nil {
				liveEditor.Stop()
			}
			ackHandle := b.takeAckHandle(chatID)
			if ctx.Err() != nil || errors.Is(ev.Err, context.Canceled) {
				if ackHandle != nil {
					b.updateAckStatus(ackHandle, jobStatusCancelled, stoppedBeforeDoneDetail)
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
				b.updateAckStatus(ackHandle, jobStatusFailed, genericFailureDetail)
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
			b.updateAckStatus(ackHandle, jobStatusCancelled, stoppedBeforeDoneDetail)
		}
		if b.controlHub != nil {
			b.controlHub.Emit(control.EvtRunFailed, control.RunEventPayload{
				ChatID: chatID,
				Error:  "cancelled",
			})
		}
		return nil
	}

	// Flush skill score updates — run completed successfully.
	tracker.flush(b.store, true)

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
			b.updateAckStatus(ackHandle, jobStatusCompleted, silentReplyDetail)
		}
		return nil
	}

	// Build the outbound message, optionally appending a usage footer.
	// Strip Active Memory recall tags so the raw injection markers never
	// leak into a Telegram reply, even if the model echoes them back.
	msg := agent.StripActiveMemoryTags(result.Message)
	usageMode, _ := b.store.GetSessionOption(chatID, "usage_mode")
	if (usageMode == "tokens" || usageMode == "full") && result.PromptTokens > 0 {
		msg += "\n\n" + FormatUsageFooter(result.PromptTokens, result.CompletionTokens)
	}
	if usageMode == "full" {
		if footer := formatMemoryContextFooter(result.MemoryContext); footer != "" {
			msg += "\n\n" + footer
		}
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

	// Rich Messages accept up to 32768 UTF-8 characters and natively render the
	// GitHub-flavored Markdown produced by the model.
	chunks := splitMessage(msg, maxTelegramRichMessageLen)

	// Mark the lifecycle placeholder as completed, then deliver the result asynchronously.
	if ackHandle := b.takeAckHandle(chatID); ackHandle != nil {
		b.updateAckStatus(ackHandle, jobStatusCompleted, "")
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
		if _, err := b.sendTelegramRichMarkdown(delivery.Chat, chunk, sendOpts); err != nil {
			log.Printf("[bot] failed to send chunk %d for chat %d: %v", i, chatID, err)
		}
	}

	// Persist to scoped daily memory.
	memoryEntry := fmt.Sprintf("Assistant (%s): %s", profileName, result.Message)
	if result.ToolUsed {
		memoryEntry += fmt.Sprintf(" [Tool: %s]", result.ToolName)
	}
	b.appendToTelegramMemory(delivery.Chat, senderIDFromMessage(delivery.Message), memoryEntry)

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

	tc := agent.NewTokenCounter()
	msgs := make([]agent.Message, len(history))
	for i, m := range history {
		msgs[i] = agent.Message{Role: m.Role, Content: m.Content}
	}

	total := tc.CountMessages(msgs)
	if total <= budget {
		return history
	}

	// Drop messages from the front (oldest) until we fit.
	// Drop in pairs to keep user/assistant alternation clean.
	for len(history) > 2 && total > budget {
		// Estimate tokens for the message being dropped.
		dropped := tc.CountTokens(history[0].Content) + 4 // +4 for message overhead
		history = history[1:]
		total -= dropped

		// If the next message forms a pair, drop it too.
		if len(history) > 0 {
			dropped = tc.CountTokens(history[0].Content) + 4
			history = history[1:]
			total -= dropped
		}
	}

	return history
}

// splitMessage breaks a long message into chunks that fit within maxLen.
// Splits on newline boundaries when possible.
func splitMessage(msg string, maxLen int) []string {
	runes := []rune(msg)
	if maxLen <= 0 || len(runes) <= maxLen {
		return []string{msg}
	}
	var chunks []string
	for len(runes) > 0 {
		if len(runes) <= maxLen {
			chunks = append(chunks, string(runes))
			break
		}
		// Find last newline before maxLen.
		cut := maxLen
		for i := maxLen - 1; i > 0; i-- {
			if runes[i] == '\n' {
				cut = i
				break
			}
		}
		chunks = append(chunks, string(runes[:cut]))
		runes = runes[cut:]
		if len(runes) > 0 && runes[0] == '\n' {
			runes = runes[1:]
		}
	}
	return chunks
}

func formatMemoryContextFooter(pack *memory.ContextPack) string {
	if pack == nil {
		return ""
	}

	footer := "Memory sources: " + pack.SourceSummary()
	if pack.Truncation.Truncated {
		footer += fmt.Sprintf(" (truncated, %d/%d chars)", pack.Truncation.UsedChars, pack.Truncation.BudgetChars)
	}
	return footer
}

// skillTracker records which skills were invoked during a run so their
// utility scores can be updated once the outcome is known.
type skillTracker struct {
	mu     sync.Mutex
	skills map[string]struct{}
}

func newSkillTracker() *skillTracker {
	return &skillTracker{skills: make(map[string]struct{})}
}

// observe checks whether a finished tool event corresponds to reading a
// SKILL.md file and, if so, records the skill name.
func (t *skillTracker) observe(event agent.ToolEvent) {
	if event.Type != agent.ToolEventFinished {
		return
	}
	if event.ToolName != "file" {
		return
	}
	// Extract path from the JSON arguments stored in event.Input.
	// The file tool receives e.g. {"command":"read","path":"…/SKILL.md"}.
	var params struct {
		Command string `json:"command"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(event.Input), &params); err != nil {
		return
	}
	if params.Command != "read" {
		return
	}
	if filepath.Base(params.Path) != "SKILL.md" {
		return
	}
	// The skill name is the parent directory of SKILL.md.
	skillName := filepath.Base(filepath.Dir(params.Path))
	if skillName == "" || skillName == "." {
		return
	}
	t.mu.Lock()
	t.skills[skillName] = struct{}{}
	t.mu.Unlock()
}

// flush records the outcome for all observed skills in the provided store.
// It is safe to call even when no skills were observed.
func (t *skillTracker) flush(store skillScoreRecorder, success bool) {
	t.mu.Lock()
	names := make([]string, 0, len(t.skills))
	for name := range t.skills {
		names = append(names, name)
	}
	t.mu.Unlock()

	for _, name := range names {
		if err := store.RecordSkillResult(name, success); err != nil {
			log.Printf("[skills] failed to record result for %q: %v", name, err)
		}
	}
}

// skillScoreRecorder is the subset of storage.Store needed for skill tracking.
type skillScoreRecorder interface {
	RecordSkillResult(skillName string, success bool) error
}
