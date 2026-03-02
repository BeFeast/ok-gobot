package bot

import (
	"context"
	"fmt"
	"log"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
)

// sessionKeyForChat returns the canonical session key for a Telegram chat.
// Private (DM) chats use "dm:<chatID>"; groups/supergroups/channels use "group:<chatID>".
func sessionKeyForChat(chat *telebot.Chat) agent.SessionKey {
	if chat.Type == telebot.ChatPrivate {
		return agent.NewDMSessionKey(chat.ID)
	}
	return agent.NewGroupSessionKey(chat.ID)
}

// processViaHub routes a user request through the RuntimeHub instead of calling
// the agent directly. Telegram becomes a pure transport adapter: it submits the
// request and then renders the resulting RunEvent.
func (b *Bot) processViaHub(ctx context.Context, c telebot.Context, sessionKey agent.SessionKey, content, session string) error {
	chatID := c.Chat().ID

	// Set chat context so the LocalCommand ApprovalFunc can send prompts to the right chat.
	b.setCurrentChatID(chatID)
	defer b.setCurrentChatID(0)

	// Resolve active agent profile and build the tool agent for this session.
	profile := b.getActiveAgentProfile(chatID)
	model := b.getAgentModel(chatID, profile)
	thinkLevel, _ := b.store.GetSessionOption(chatID, "think_level")
	if thinkLevel == "" {
		thinkLevel = b.aiConfig.DefaultThinking
	}
	aiClient := b.getAIClientForModelAndThinkLevel(model, thinkLevel)
	toolAgent := b.createAgentToolAgent(chatID, profile, aiClient)
	if thinkLevel != "" {
		toolAgent.SetThinkLevel(thinkLevel)
	}

	// Wire PlaceholderEditor for live tool-event status lines.
	// The ⏳ ack message (sent upfront in the message handler) is updated as
	// each tool starts/finishes; at the end processViaHub overwrites it with
	// the final response text.
	if ackHandle := b.ackManager.Peek(chatID); ackHandle != nil {
		placeholder := NewPlaceholderEditor(b.api, ackHandle.Message)
		toolAgent.SetToolEventCallback(func(event agent.ToolEvent) {
			placeholder.OnToolEvent(event)
		})
	}

	// Start typing indicator while the hub is running.
	stopTyping := NewTypingIndicator(b.api, c.Chat())
	defer stopTyping()

	// Submit to the hub — execution happens asynchronously in the hub's goroutine.
	req := agent.RunRequest{
		SessionKey: sessionKey,
		Content:    content,
		Session:    session,
		Agent:      toolAgent,
		Context:    ctx,
	}
	events := b.hub.Submit(req)

	// Wait for the single result event.
	var result *agent.AgentResponse
	for ev := range events {
		switch ev.Type {
		case agent.RunEventDone:
			result = ev.Result

		case agent.RunEventError:
			stopTyping()
			ackMsg := b.takeAck(chatID)
			if ctx.Err() != nil {
				// Cancelled — silently clear the ⏳ placeholder.
				if ackMsg != nil {
					b.api.Delete(ackMsg) //nolint:errcheck
				}
				return nil
			}
			log.Printf("[bot] hub error for session %s: %v", sessionKey, ev.Err)
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
		if ackMsg := b.takeAck(chatID); ackMsg != nil {
			b.api.Delete(ackMsg) //nolint:errcheck
		}
		return nil
	}

	// Record token usage.
	if result.PromptTokens > 0 || result.CompletionTokens > 0 {
		b.store.UpdateTokenUsage(chatID, result.PromptTokens, result.CompletionTokens, result.TotalTokens)
	}

	// Suppress internal sentinel tokens.
	trimmed := strings.TrimSpace(result.Message)
	if trimmed == "SILENT_REPLY" || trimmed == "HEARTBEAT_OK" {
		log.Printf("[bot] agent '%s' returned silent token: %s — suppressing reply", profile.Name, trimmed)
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
	memoryEntry := fmt.Sprintf("Assistant (%s): %s", profile.Name, result.Message)
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

	log.Printf("[bot] session %s processed by agent '%s'", sessionKey, profile.Name)
	return nil
}
