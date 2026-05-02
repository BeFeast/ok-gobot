package bot

import (
	"fmt"
	"strings"
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

func formatTelegramJobHeader(jobID string, status telegramJobStatus) string {
	icon := "🧠"
	label := "Working..."
	switch status {
	case jobStatusAccepted:
		label = "Working..."
	case jobStatusQueued:
		icon = "⏳"
		label = "Queued..."
	case jobStatusRunning:
		label = "Working..."
	case jobStatusCompleted:
		icon = "✅"
		label = "Done."
	case jobStatusFailed:
		icon = "❌"
		label = "Failed."
	case jobStatusCancelled:
		icon = "🛑"
		label = "Cancelled."
	}

	return fmt.Sprintf("%s %s", icon, label)
}

func formatTelegramJobStatus(jobID string, status telegramJobStatus, detail string) string {
	header := formatTelegramJobHeader(jobID, status)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return header
	}
	if header == "" {
		return detail
	}
	return header + "\n" + detail
}
