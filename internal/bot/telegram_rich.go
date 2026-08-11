package bot

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"gopkg.in/telebot.v4"
)

const maxTelegramRichMessageLen = 32768

// sendTelegramRichMarkdown delivers GitHub-flavored model output through the
// Bot API 10.1 sendRichMessage method. Telegram parses Rich Markdown into
// native headings, lists, tables, quotes, links and code blocks.
func (b *Bot) sendTelegramRichMarkdown(to telebot.Recipient, markdown string, opts ...interface{}) (*telebot.Message, error) {
	if b == nil || b.api == nil {
		return nil, fmt.Errorf("Telegram bot is not configured")
	}

	message, err := b.api.Send(to, &telebot.InputRichMessage{Markdown: markdown}, opts...)
	if err == nil {
		return message, nil
	}

	// A 400/404 response is an authoritative API rejection: no rich message was
	// delivered, so falling back cannot duplicate a successful send. Transport,
	// rate-limit and authorization errors are returned without a second send.
	if !isTelegramRichMessageRejection(err) {
		return nil, err
	}

	log.Printf("[telegram] Rich Markdown rejected; falling back to plain text: %v", err)
	return b.api.Send(to, markdown, opts...)
}

func isTelegramRichMessageRejection(err error) bool {
	var telegramErr *telebot.Error
	if errors.As(err, &telegramErr) {
		return telegramErr.Code == http.StatusBadRequest || telegramErr.Code == http.StatusNotFound
	}

	// Telebot represents previously unseen Bot API descriptions as a formatted
	// error instead of *telebot.Error, while retaining the API status suffix.
	message := err.Error()
	return strings.HasSuffix(message, " (400)") || strings.HasSuffix(message, " (404)")
}
