package bot

import (
	"fmt"
	"log"

	"gopkg.in/telebot.v4"
)

// ConfigWatcher defines the interface for config hot-reload
type ConfigWatcher interface {
	TriggerReload() error
	Stop()
}

// SetConfigWatcher sets the config watcher for the bot
func (b *Bot) SetConfigWatcher(watcher ConfigWatcher) {
	b.configWatcher = watcher
}

// RegisterReloadCommand registers the /reload command handler
func (b *Bot) RegisterReloadCommand() {
	b.api.Handle("/reload", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleReloadCommand(c)
	}))
}

// handleReloadCommand handles the /reload command (admin only)
func (b *Bot) handleReloadCommand(c telebot.Context) error {
	userID := c.Sender().ID

	// Check if user is admin
	if !b.authManager.IsAdmin(userID) {
		return c.Send("🔒 This command is only available to administrators.")
	}

	// Check if config watcher is set
	if b.configWatcher == nil {
		return c.Send("⚠️ Config hot-reload is not enabled.")
	}

	// Trigger reload
	if err := b.configWatcher.TriggerReload(); err != nil {
		log.Printf("Config reload failed: %v", err)
		return c.Send(fmt.Sprintf("❌ Failed to reload config: %v", err))
	}

	return c.Send("✅ Configuration reloaded successfully!")
}
