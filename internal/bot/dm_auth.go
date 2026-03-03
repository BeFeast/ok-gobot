package bot

import (
	"encoding/json"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/logger"
)

const unauthorizedDMMessage = "🔒 Not authorized. Please contact the bot administrator."

// DenyAuditEntry is a structured record emitted for every unauthorized access
// attempt. It is serialised to JSON on a single log line for easy ingestion by
// log aggregators.
type DenyAuditEntry struct {
	Timestamp int64  `json:"ts"`        // Unix timestamp of the attempt
	SenderID  int64  `json:"sender_id"` // Telegram user ID
	Username  string `json:"username"`  // @handle (empty if not set)
	ChatID    int64  `json:"chat_id"`   // Telegram chat ID
	ChatType  string `json:"chat_type"` // "private", "group", "supergroup", "channel"
}

// logDeniedAccess emits a WARN-level structured audit entry for a denied access
// attempt. It must be called before any session/transcript state is written so
// there are zero side-effects on agent:main:main or any other runtime session.
func logDeniedAccess(senderID int64, username string, chatID int64, chatType string) {
	entry := DenyAuditEntry{
		Timestamp: time.Now().Unix(),
		SenderID:  senderID,
		Username:  username,
		ChatID:    chatID,
		ChatType:  chatType,
	}

	data, _ := json.Marshal(entry)
	logger.Warnf("[AUDIT] deny %s", data)
}

func (b *Bot) denyUnauthorizedDirectMessage(msg *telebot.Message) bool {
	if msg == nil || msg.Chat == nil || msg.Sender == nil {
		return false
	}
	if msg.Chat.Type != telebot.ChatPrivate {
		return false
	}
	if b.authManager.CheckDirectMessageAccess(msg.Sender.ID) {
		return false
	}

	logDeniedAccess(msg.Sender.ID, msg.Sender.Username, msg.Chat.ID, string(msg.Chat.Type))
	return true
}

func (b *Bot) guardUnauthorizedDM(allowUnauthorizedDM bool, handler telebot.HandlerFunc) telebot.HandlerFunc {
	return func(c telebot.Context) error {
		if !allowUnauthorizedDM && b.denyUnauthorizedDirectMessage(c.Message()) {
			return c.Send(unauthorizedDMMessage)
		}
		return handler(c)
	}
}
