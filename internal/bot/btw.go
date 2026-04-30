package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
)

// handleBtwCommand handles /btw side queries during active task execution.
// Inspired by Claude Code's /btw feature (Boris Cherny tip #9):
// the user can ask a quick question while an agent is working on a main task,
// and the side query runs independently without interrupting the main task.
//
// Usage: reply to any bot message with "/btw <question>"
func (b *Bot) handleBtwCommand(c telebot.Context) error {
	question := strings.TrimSpace(c.Message().Payload)
	if question == "" {
		return c.Send("💬 Usage: /btw <your question>\n\nAsk a quick side question while the agent works on the main task.")
	}

	chatID := c.Chat().ID
	sessionKey := sessionKeyForChat(c.Chat())
	model := b.getEffectiveModel(chatID)

	// Load current session history to give the side query access to context.
	var history []ai.ChatMessage
	if v2Msgs, err := b.store.GetSessionMessagesV2(string(sessionKey), 500); err == nil && len(v2Msgs) > 0 {
		for _, m := range v2Msgs {
			history = append(history, ai.ChatMessage{Role: m.Role, Content: m.Content})
		}
		history = buildRunHistory(history, question, model)
	}

	// Send an immediate acknowledgment so the user knows the query was received.
	ackMsg, err := b.api.Send(c.Chat(), "💬 _Answering..._", &telebot.SendOptions{
		ParseMode: telebot.ModeMarkdown,
	})
	if err != nil {
		log.Printf("[btw] failed to send ack for chat=%d: %v", chatID, err)
	}

	chat := c.Chat()

	go func() {
		// Use a unique session key so the hub does not cancel the main run.
		btwKey := agent.SessionKey(fmt.Sprintf("btw:%d:%d", chatID, time.Now().UnixNano()))
		userID := int64(0)
		if c.Sender() != nil {
			userID = c.Sender().ID
		}

		session, _ := b.store.GetSession(chatID)
		events := b.hub.Submit(agent.RunRequest{
			SessionKey: btwKey,
			ChatID:     chatID,
			UserID:     userID,
			ChatType:   string(chat.Type),
			Content:    question,
			Session:    session,
			History:    history,
			Context:    context.Background(),
		})

		var result *agent.AgentResponse
		for ev := range events {
			if ev.Type == agent.RunEventDone {
				result = ev.Result
			}
		}

		if result == nil {
			if ackMsg != nil {
				b.api.Edit(ackMsg, "❌ Side query failed.") //nolint:errcheck
			}
			return
		}

		answer := strings.TrimSpace(result.Message)
		if answer == "" || answer == "SILENT_REPLY" {
			if ackMsg != nil {
				b.api.Edit(ackMsg, "💬 _(no answer)_", &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}) //nolint:errcheck
			}
			return
		}

		// Edit the ack placeholder with the actual answer.
		reply := fmt.Sprintf("💬 *Side query:* _%s_\n\n%s", escapeMarkdown(question), answer)
		if ackMsg != nil {
			if _, err := b.api.Edit(ackMsg, reply, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
				// Edit failed (e.g. message too old) — send a fresh message.
				if _, err2 := b.api.Send(chat, reply, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err2 != nil {
					log.Printf("[btw] failed to deliver answer for chat=%d: %v", chatID, err2)
				}
			}
		} else {
			if _, err := b.api.Send(chat, reply, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
				log.Printf("[btw] failed to deliver answer for chat=%d: %v", chatID, err)
			}
		}
	}()

	return nil
}

// escapeMarkdown escapes special characters for Telegram MarkdownV1.
func escapeMarkdown(s string) string {
	s = strings.ReplaceAll(s, "_", "\\_")
	s = strings.ReplaceAll(s, "*", "\\*")
	s = strings.ReplaceAll(s, "[", "\\[")
	s = strings.ReplaceAll(s, "`", "\\`")
	return s
}
