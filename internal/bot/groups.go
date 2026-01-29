package bot

import (
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/storage"
)

// ActivationMode represents how the bot responds in groups
type ActivationMode string

const (
	// ModeActive means bot responds to all messages
	ModeActive ActivationMode = "active"
	// ModeStandby means bot only responds when mentioned or replied to
	ModeStandby ActivationMode = "standby"
)

// GroupManager handles group chat activation modes
type GroupManager struct {
	store       *storage.Store
	defaultMode ActivationMode
	botUsername string
}

// NewGroupManager creates a new group manager
func NewGroupManager(store *storage.Store, defaultMode string, botUsername string) *GroupManager {
	mode := ModeStandby
	if defaultMode == "active" {
		mode = ModeActive
	}

	return &GroupManager{
		store:       store,
		defaultMode: mode,
		botUsername: botUsername,
	}
}

// GetMode returns the activation mode for a group
func (gm *GroupManager) GetMode(chatID int64) ActivationMode {
	mode, err := gm.store.GetGroupMode(chatID)
	if err != nil || mode == "" {
		return gm.defaultMode
	}

	if mode == "active" {
		return ModeActive
	}
	return ModeStandby
}

// SetMode sets the activation mode for a group
func (gm *GroupManager) SetMode(chatID int64, mode ActivationMode) error {
	return gm.store.SetGroupMode(chatID, string(mode))
}

// ShouldRespond determines if the bot should respond to a message
func (gm *GroupManager) ShouldRespond(chatID int64, msg *telebot.Message, botUsername string) bool {
	// Always respond in private chats
	if msg.Chat.Type == telebot.ChatPrivate {
		return true
	}

	// Get the mode for this group
	mode := gm.GetMode(chatID)

	// In active mode, respond to all messages
	if mode == ModeActive {
		return true
	}

	// In standby mode, check if bot is mentioned or replied to
	return gm.isBotMentioned(msg, botUsername)
}

// isBotMentioned checks if the bot is mentioned in the message
func (gm *GroupManager) isBotMentioned(msg *telebot.Message, botUsername string) bool {
	// Check if message is a reply to bot
	if msg.ReplyTo != nil && msg.ReplyTo.Sender != nil {
		if msg.ReplyTo.Sender.IsBot && msg.ReplyTo.Sender.Username == botUsername {
			return true
		}
	}

	// Check for @mention in entities
	if msg.Entities != nil {
		for _, entity := range msg.Entities {
			if entity.Type == telebot.EntityMention || entity.Type == telebot.EntityTMention {
				// Extract the mention text
				mentionText := msg.Text[entity.Offset : entity.Offset+entity.Length]
				if strings.TrimPrefix(mentionText, "@") == botUsername {
					return true
				}
			}
		}
	}

	// Check if message starts with bot name (case-insensitive)
	lowerText := strings.ToLower(strings.TrimSpace(msg.Text))
	lowerBotUsername := strings.ToLower(botUsername)
	if strings.HasPrefix(lowerText, lowerBotUsername) {
		return true
	}
	if strings.HasPrefix(lowerText, "@"+lowerBotUsername) {
		return true
	}

	return false
}
