package bot

import (
	"fmt"
	"ok-gobot/internal/tessera"
	"time"

	"gopkg.in/telebot.v4"
)

type telegramJobStatus string

const (
	jobStatusAccepted  telegramJobStatus = "accepted"
	jobStatusQueued    telegramJobStatus = "queued"
	jobStatusRunning   telegramJobStatus = "running"
	jobStatusCompleted telegramJobStatus = "completed"
	jobStatusFailed    telegramJobStatus = "failed"
	jobStatusCancelled telegramJobStatus = "cancelled"
)

type telegramDelivery struct {
	Turn    *tessera.Turn
	Chat    *telebot.Chat
	Message *telebot.Message
}

func newTelegramDelivery(c telebot.Context) telegramDelivery {
	return telegramDelivery{
		Chat:    c.Chat(),
		Message: c.Message(),
	}
}

func newTelegramJobID(chatID int64, messageID int) string {
	if messageID > 0 {
		return fmt.Sprintf("tg-%d-%d", messageID, time.Now().UnixNano())
	}
	return fmt.Sprintf("tg-%d-%d", chatID, time.Now().UnixNano())
}

// formatTelegramJobStatus renders a lifecycle state for the chat. The
// internal job id is deliberately absent: it lives in /jobs and logs.
func formatTelegramJobStatus(status telegramJobStatus, detail string) string {
	phrase := telegramStatusPhrase(status)
	if detail == "" {
		return phrase
	}
	return phrase + "\n" + detail
}

func (b *Bot) newTesseraDelivery(c telebot.Context) telegramDelivery {
	d := newTelegramDelivery(c)
	if b.tessera != nil {
		d.Turn = telegramTurn(c)
	}
	return d
}
