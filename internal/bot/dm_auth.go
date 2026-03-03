package bot

import (
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/logger"
)

const unauthorizedDMMessage = "🔒 Not authorized. Please contact the bot administrator."

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

	b.auditUnauthorizedDirectMessage(msg)
	return true
}

func (b *Bot) auditUnauthorizedDirectMessage(msg *telebot.Message) {
	logger.Warnf(
		"audit=telegram_dm_auth_deny timestamp=%s sender_id=%d chat_id=%d channel=telegram_dm chat_type=%s username=%q reason=unauthorized_dm_sender",
		time.Now().UTC().Format(time.RFC3339),
		msg.Sender.ID,
		msg.Chat.ID,
		msg.Chat.Type,
		msg.Sender.Username,
	)
}

func (b *Bot) guardUnauthorizedDM(allowUnauthorizedDM bool, handler telebot.HandlerFunc) telebot.HandlerFunc {
	return func(c telebot.Context) error {
		if !allowUnauthorizedDM && b.denyUnauthorizedDirectMessage(c.Message()) {
			return c.Send(unauthorizedDMMessage)
		}
		return handler(c)
	}
}
