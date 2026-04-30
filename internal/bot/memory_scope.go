package bot

import (
	"fmt"
	"log"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/memory"
)

func (b *Bot) appendToTelegramMemory(chat *telebot.Chat, userID int64, content string) {
	if b == nil || b.memory == nil {
		return
	}
	scopeDir := telegramMemoryScopeDir(chat, userID)
	if err := b.memory.AppendToScopedToday(scopeDir, content); err != nil {
		log.Printf("[bot] failed to save scoped memory: %v", err)
	}
}

func (b *Bot) appendQuickNoteToTelegramMemory(chat *telebot.Chat, userID int64, content string) error {
	if b == nil || b.memory == nil {
		return nil
	}
	return b.memory.AppendScopedQuickNoteToToday(telegramMemoryScopeDir(chat, userID), content)
}

func (b *Bot) scopedTodayNote(chat *telebot.Chat, userID int64) (*agent.DailyNote, error) {
	return b.memory.GetScopedTodayNote(telegramMemoryScopeDir(chat, userID))
}

func telegramMemoryScopeDir(chat *telebot.Chat, userID int64) string {
	if chat == nil {
		return ""
	}
	if chat.Type == telebot.ChatPrivate {
		if userID == 0 {
			userID = chat.ID
		}
		return fmt.Sprintf("users/%d", userID)
	}
	return fmt.Sprintf("chats/%d", chat.ID)
}

func (b *Bot) memoryRecallContext(chatID, userID int64, chatType string, sessionKey agent.SessionKey) memory.RecallContext {
	allowGroup := false
	if chatType == string(telebot.ChatGroup) || chatType == string(telebot.ChatSuperGroup) || chatType == string(telebot.ChatChannel) {
		allowGroup = b.groupManager != nil && b.groupManager.GetMode(chatID) == ModeActive
	}
	allowGlobal := b.adminID != 0 && userID == b.adminID && chatType == string(telebot.ChatPrivate)

	return memory.RecallContext{
		UserID:             userID,
		ChatID:             chatID,
		SessionKey:         string(sessionKey),
		ChatType:           chatType,
		AllowGroupChat:     allowGroup,
		AllowGlobalPrivate: allowGlobal,
		ExtraPathLabels:    b.memoryExtraPathLabels,
	}
}

func senderIDFromMessage(msg *telebot.Message) int64 {
	if msg == nil || msg.Sender == nil {
		return 0
	}
	return msg.Sender.ID
}
