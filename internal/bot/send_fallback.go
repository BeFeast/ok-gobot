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

// sendMarkdownWithPlainFallback delivers text as Markdown and falls back to
// plain text when Telegram rejects the markup, so an answer is never lost to
// formatting (2026-08-21: a completion notification died on a bad entity and
// the user saw an eternal "Working on it" placeholder).
func sendMarkdownWithPlainFallback(api *telebot.Bot, to telebot.Recipient, text string) (*telebot.Message, error) {
	msg, err := api.Send(to, text, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	if telegramRejectedEntities(err) {
		log.Printf("[send] markdown rejected (%v) — resending as plain text", err)
		return api.Send(to, text)
	}
	return msg, err
}

// editMarkdownWithPlainFallback is the same contract for message edits.
func editMarkdownWithPlainFallback(api *telebot.Bot, msg telebot.Editable, text string) (*telebot.Message, error) {
	m, err := api.Edit(msg, text, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	if telegramRejectedEntities(err) {
		log.Printf("[edit] markdown rejected (%v) — editing as plain text", err)
		return api.Edit(msg, text)
	}
	return m, err
}
