package bot

import (
	"fmt"
	"log"
	"strings"

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

	// Bootstrap (soul files + installed skills) always reloads, and the
	// Telegram command menu is re-registered so newly installed skills show up.
	var parts []string
	if b.personality != nil {
		if err := b.personality.Reload(); err != nil {
			log.Printf("Bootstrap reload failed: %v", err)
			return c.Send(fmt.Sprintf("❌ Failed to reload bootstrap: %v", err))
		}
		parts = append(parts, "bootstrap")
	}
	b.RefreshCommands()
	parts = append(parts, fmt.Sprintf("%d commands registered", len(b.builtinCommands())+len(b.skillCommandList())))

	// Config hot-reload is optional: only when a watcher is wired.
	if b.configWatcher != nil {
		if err := b.configWatcher.TriggerReload(); err != nil {
			log.Printf("Config reload failed: %v", err)
			return c.Send(fmt.Sprintf("❌ Failed to reload config: %v", err))
		}
		parts = append(parts, "config")
	}

	return c.Send("✅ Reloaded: " + strings.Join(parts, ", "))
}
