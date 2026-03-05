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
)

// sessionKeyForChat returns the canonical session key for a Telegram chat.
// Private (DM) chats use "dm:<chatID>"; groups/supergroups/channels use "group:<chatID>".
func sessionKeyForChat(chat *telebot.Chat) agent.SessionKey {
	if chat.Type == telebot.ChatPrivate {
		return agent.NewDMSessionKey(chat.ID)
	}
	return agent.NewGroupSessionKey(chat.ID)
}

// processViaHub submits an inbound envelope to the RuntimeHub and renders the
// resulting events back to Telegram. The bot is a thin transport adapter here:
// all agent creation, tool execution, and run orchestration happen inside the hub.
func (b *Bot) processViaHub(ctx context.Context, c telebot.Context, sessionKey agent.SessionKey, content, session string) error {
	return b.processViaHubWithContent(ctx, c, sessionKey, content, nil, session)
}

func (b *Bot) processViaHubWithContent(
	ctx context.Context,
	c telebot.Context,
	sessionKey agent.SessionKey,
	content string,
	userContent []ai.ContentBlock,
	session string,
) error {
	chatID := c.Chat().ID

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
		liveEditor = NewLiveStreamEditor(b.api, ackHandle.Message)
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
	stopTyping := NewTypingIndicator(b.api, c.Chat())
	defer stopTyping()

	// Submit to the hub — the hub owns agent resolution, tool execution,
	// and run lifecycle. We only provide the inbound envelope.
	req := agent.RunRequest{
		SessionKey:   sessionKey,
		ChatID:       chatID,
		Content:      content,
		UserContent:  userContent,
		Session:      session,
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
			ackMsg := b.takeAck(chatID)
			if ctx.Err() != nil || errors.Is(ev.Err, context.Canceled) {
				// Cancelled (by /abort, /stop, or app shutdown) — silently clear the ⏳ placeholder.
				if ackMsg != nil {
					b.api.Delete(ackMsg) //nolint:errcheck
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
			if ackMsg != nil {
				if _, err := b.api.Edit(ackMsg, errText); err != nil {
					b.api.Send(c.Chat(), errText) //nolint:errcheck
				}
			} else {
				b.api.Send(c.Chat(), errText) //nolint:errcheck
			}
			return nil
		}
	}

	if result == nil {
		// Run was cancelled before producing a result.
		if liveEditor != nil {
			liveEditor.Stop()
		}
		if ackMsg := b.takeAck(chatID); ackMsg != nil {
			b.api.Delete(ackMsg) //nolint:errcheck
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
		if ackMsg := b.takeAck(chatID); ackMsg != nil {
			b.api.Delete(ackMsg) //nolint:errcheck
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
	if len(reactions) > 0 && c.Message() != nil {
		for _, emoji := range reactions {
			if err := b.api.React(c.Chat(), c.Message(), telebot.Reactions{
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

	// Edit the ⏳ placeholder if one exists; otherwise send a new message.
	ackMsg := b.takeAck(chatID)
	if ackMsg != nil {
		if _, err := b.api.Edit(ackMsg, msg); err != nil {
			log.Printf("[bot] failed to edit ⏳ for chat %d: %v", chatID, err)
			b.api.Send(c.Chat(), msg) //nolint:errcheck
		}
	} else {
		sendOpts := &telebot.SendOptions{}
		switch {
		case replyTarget.MessageID == -1:
			sendOpts.ReplyTo = c.Message()
		case replyTarget.MessageID > 0:
			sendOpts.ReplyTo = &telebot.Message{ID: replyTarget.MessageID}
		}
		if _, err := b.api.Send(c.Chat(), msg, sendOpts); err != nil {
			return fmt.Errorf("send response: %w", err)
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

	// Persist session state.
	if err := b.store.SaveSession(chatID, result.Message); err != nil {
		log.Printf("[bot] failed to save session: %v", err)
	}

	log.Printf("[bot] session %s processed (agent: %s)", sessionKey, profileName)
	return nil
}
