package bot

import (
	"context"
	"log"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/logger"
)

// registerMigrationHandler sets up the group->supergroup migration handler
func (b *Bot) registerMigrationHandler(ctx context.Context) {
	b.api.Handle(telebot.OnMigration, func(c telebot.Context) error {
		return b.handleMigration(c)
	})
}

// handleMigration handles group-to-supergroup migration
// When a Telegram group becomes a supergroup, the chat ID changes.
// We need to migrate all stored data to the new chat ID.
func (b *Bot) handleMigration(c telebot.Context) error {
	msg := c.Message()
	oldChatID := msg.Chat.ID
	newChatID := msg.MigrateTo

	if newChatID == 0 {
		return nil // Not a migration event
	}

	logger.Debugf("Bot: group migration from %d to %d", oldChatID, newChatID)
	log.Printf("Group migration: %d → %d", oldChatID, newChatID)

	// Migrate session data
	if err := b.migrateSessionData(oldChatID, newChatID); err != nil {
		log.Printf("Failed to migrate session data: %v", err)
	}

	// Migrate group mode
	mode, err := b.store.GetGroupMode(oldChatID)
	if err == nil && mode != "" {
		b.store.SetGroupMode(newChatID, mode)
		log.Printf("Migrated group mode '%s' to new chat %d", mode, newChatID)
	}

	return nil
}

// migrateSessionData copies session data from old to new chat ID
func (b *Bot) migrateSessionData(oldChatID, newChatID int64) error {
	// Get old session state
	state, err := b.store.GetSession(oldChatID)
	if err != nil {
		return err
	}

	// Save to new chat ID
	if state != "" {
		if err := b.store.SaveSession(newChatID, state); err != nil {
			return err
		}
	}

	// Migrate model override
	override, err := b.store.GetModelOverride(oldChatID)
	if err == nil && override != "" {
		b.store.SetModelOverride(newChatID, override)
	}

	// Migrate active agent
	agent, err := b.store.GetActiveAgent(oldChatID)
	if err == nil && agent != "" && agent != "default" {
		b.store.SetActiveAgent(newChatID, agent)
	}

	log.Printf("Migrated session data from chat %d to %d", oldChatID, newChatID)
	return nil
}
