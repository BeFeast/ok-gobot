package bot

import (
	"log"
	"strings"

	"gopkg.in/telebot.v4"
)

// telegramRejectedEntities reports whether err is Telegram's "can't parse
// entities" 400 — well-formed text with markup Telegram cannot parse.
func telegramRejectedEntities(err error) bool {
	return err != nil && strings.Contains(err.Error(), "can't parse entities")
}

// sendMarkdownWithPlainFallback renders LLM markdown to Telegram HTML and
// delivers it, falling back to plain text if Telegram still rejects the markup,
// so an answer is never lost to formatting (2026-08-21: a completion
// notification died on a bad entity and the user saw an eternal "Working on it"
// placeholder; then the plain fallback showed raw ## / ** markup).
func sendMarkdownWithPlainFallback(api *telebot.Bot, to telebot.Recipient, text string) (*telebot.Message, error) {
	msg, err := api.Send(to, renderTelegramHTML(text), &telebot.SendOptions{ParseMode: telebot.ModeHTML})
	if telegramRejectedEntities(err) {
		log.Printf("[send] html rejected (%v) — resending as plain text", err)
		return api.Send(to, text)
	}
	return msg, err
}

// editMarkdownWithPlainFallback is the same contract for message edits.
func editMarkdownWithPlainFallback(api *telebot.Bot, msg telebot.Editable, text string) (*telebot.Message, error) {
	m, err := api.Edit(msg, renderTelegramHTML(text), &telebot.SendOptions{ParseMode: telebot.ModeHTML})
	if telegramRejectedEntities(err) {
		log.Printf("[edit] html rejected (%v) — editing as plain text", err)
		return api.Edit(msg, text)
	}
	return m, err
}
