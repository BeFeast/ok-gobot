package bot

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
)

// registerExtraHandlers registers all additional command handlers
func (b *Bot) registerExtraHandlers() {
	b.api.Handle("/whoami", func(c telebot.Context) error {
		return b.handleWhoamiCommand(c)
	})

	b.api.Handle("/commands", func(c telebot.Context) error {
		return b.handleCommandsCommand(c)
	})

	b.api.Handle("/new", func(c telebot.Context) error {
		return b.handleNewCommand(c)
	})

	b.api.Handle("/stop", func(c telebot.Context) error {
		return b.handleStopCommand(c)
	})

	b.api.Handle("/usage", func(c telebot.Context) error {
		return b.handleUsageCommand(c)
	})

	b.api.Handle("/context", func(c telebot.Context) error {
		return b.handleContextCommand(c)
	})

	b.api.Handle("/compact", func(c telebot.Context) error {
		return b.handleCompactCommand(c)
	})

	b.api.Handle("/think", func(c telebot.Context) error {
		return b.handleThinkCommand(c)
	})

	b.api.Handle("/verbose", func(c telebot.Context) error {
		return b.handleVerboseCommand(c)
	})

	b.api.Handle("/queue", func(c telebot.Context) error {
		return b.handleQueueCommand(c)
	})

	b.api.Handle("/tts", func(c telebot.Context) error {
		return b.handleTTSCommand(c)
	})

	b.api.Handle("/restart", func(c telebot.Context) error {
		return b.handleRestartCommand(c)
	})
}

// handleWhoamiCommand shows sender info
func (b *Bot) handleWhoamiCommand(c telebot.Context) error {
	sender := c.Sender()
	chat := c.Chat()

	var sb strings.Builder
	sb.WriteString("👤 *Who am I:*\n\n")
	sb.WriteString(fmt.Sprintf("🆔 User ID: `%d`\n", sender.ID))
	if sender.Username != "" {
		sb.WriteString(fmt.Sprintf("👤 Username: @%s\n", sender.Username))
	}
	if sender.FirstName != "" {
		name := sender.FirstName
		if sender.LastName != "" {
			name += " " + sender.LastName
		}
		sb.WriteString(fmt.Sprintf("📛 Name: %s\n", name))
	}
	sb.WriteString(fmt.Sprintf("💬 Chat ID: `%d`\n", chat.ID))
	sb.WriteString(fmt.Sprintf("📋 Chat Type: %s\n", chat.Type))

	if b.authManager.IsAdmin(sender.ID) {
		sb.WriteString("\n🔑 Role: admin")
	} else if b.authManager.CheckAccess(sender.ID, chat.ID) {
		sb.WriteString("\n🔑 Role: authorized")
	} else {
		sb.WriteString("\n🔒 Role: unauthorized")
	}

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleCommandsCommand lists all slash commands
func (b *Bot) handleCommandsCommand(c telebot.Context) error {
	commands := []struct{ cmd, desc string }{
		{"help", "Show available commands"},
		{"commands", "List all slash commands"},
		{"status", "Show current status"},
		{"whoami", "Show your sender info"},
		{"new", "Start a new session"},
		{"clear", "Clear conversation history"},
		{"stop", "Stop the current run"},
		{"memory", "Show today's memory"},
		{"tools", "List available tools"},
		{"model", "Show or set AI model"},
		{"agent", "Manage agents"},
		{"usage", "Usage footer control (off/tokens/full)"},
		{"context", "Explain how context is built"},
		{"compact", "Compact session context"},
		{"think", "Set thinking level (off/low/medium/high)"},
		{"verbose", "Toggle verbose mode (on/off)"},
		{"queue", "Adjust queue settings"},
		{"tts", "Control text-to-speech"},
		{"activate", "Activate bot in group"},
		{"standby", "Set standby mode in group"},
		{"pair", "Pair with bot using code"},
		{"auth", "Authorization management (admin)"},
		{"reload", "Reload configuration (admin)"},
		{"restart", "Restart the bot (admin)"},
	}

	var sb strings.Builder
	sb.WriteString("📋 *All Commands:*\n\n")
	for _, cmd := range commands {
		sb.WriteString(fmt.Sprintf("/%s — %s\n", cmd.cmd, cmd.desc))
	}

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleNewCommand starts a new session
func (b *Bot) handleNewCommand(c telebot.Context) error {
	chatID := c.Chat().ID

	if err := b.store.ResetSession(chatID); err != nil {
		log.Printf("Failed to reset session: %v", err)
		return c.Send("❌ Failed to start new session")
	}

	return c.Send("✅ New session started. History and counters cleared.")
}

// handleStopCommand stops the current AI run
func (b *Bot) handleStopCommand(c telebot.Context) error {
	chatID := c.Chat().ID

	b.cancelMu.Lock()
	cancel, ok := b.activeRuns[chatID]
	b.cancelMu.Unlock()

	if ok && cancel != nil {
		cancel()
		return c.Send("🛑 Stopped current run.")
	}
	return c.Send("ℹ️ No active run to stop.")
}

// handleUsageCommand controls usage footer display
func (b *Bot) handleUsageCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	args := strings.TrimSpace(c.Message().Payload)

	if args == "" {
		mode, _ := b.store.GetSessionOption(chatID, "usage_mode")
		if mode == "" {
			mode = "off"
		}
		return c.Send(fmt.Sprintf("📊 Usage display: `%s`\n\nOptions: `/usage off` | `/usage tokens` | `/usage full`", mode),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	switch args {
	case "off", "tokens", "full":
		if err := b.store.SetSessionOption(chatID, "usage_mode", args); err != nil {
			return c.Send("❌ Failed to set usage mode")
		}
		return c.Send(fmt.Sprintf("✅ Usage display set to: `%s`", args),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	default:
		return c.Send("❌ Invalid mode. Use: off, tokens, full")
	}
}

// handleContextCommand explains how context is built
func (b *Bot) handleContextCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	usage, _ := b.store.GetTokenUsage(chatID)

	var sb strings.Builder
	sb.WriteString("📚 *Context Structure:*\n\n")

	// System prompt components
	sb.WriteString("*System Prompt Components:*\n")
	prompt := b.personality.GetSystemPrompt()
	sb.WriteString(fmt.Sprintf("• Personality (SOUL.md, IDENTITY.md, etc.): ~%d chars\n", len(prompt)))

	// Tools
	toolCount := len(b.toolAgent.GetAvailableTools())
	sb.WriteString(fmt.Sprintf("• Tools: %d registered\n", toolCount))

	// Memory
	sb.WriteString("• Daily memory: today + yesterday notes\n")

	// Session info
	sb.WriteString(fmt.Sprintf("\n*Session:*\n"))
	sb.WriteString(fmt.Sprintf("• Messages: %d\n", usage.MessageCount))
	sb.WriteString(fmt.Sprintf("• Compactions: %d\n", usage.CompactionCount))

	// Token budget
	contextLimit := agent.ModelLimits(b.aiConfig.Model)
	sb.WriteString(fmt.Sprintf("\n*Token Budget:*\n"))
	if usage.TotalTokens > 0 {
		pct := float64(usage.TotalTokens) / float64(contextLimit) * 100
		sb.WriteString(fmt.Sprintf("• Used: %s / %s (%.0f%%)\n",
			formatTokenCount(usage.TotalTokens), formatTokenCount(contextLimit), pct))
	} else {
		sb.WriteString(fmt.Sprintf("• Context limit: %s\n", formatTokenCount(contextLimit)))
	}

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleCompactCommand manually compacts session context
func (b *Bot) handleCompactCommand(c telebot.Context) error {
	chatID := c.Chat().ID

	// Get recent messages
	messages, err := b.store.GetSessionMessages(chatID, 100)
	if err != nil || len(messages) == 0 {
		return c.Send("ℹ️ No conversation to compact.")
	}

	return c.Send("🧹 Compaction not yet implemented. Use /new to start fresh.")
}

// handleThinkCommand controls thinking level
func (b *Bot) handleThinkCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	args := strings.TrimSpace(c.Message().Payload)

	if args == "" {
		level, _ := b.store.GetSessionOption(chatID, "think_level")
		if level == "" {
			level = "(default)"
		}
		return c.Send(fmt.Sprintf("🧠 Think level: `%s`\n\nOptions: `/think off` | `/think low` | `/think medium` | `/think high`", level),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	validLevels := map[string]bool{"off": true, "low": true, "medium": true, "high": true}
	if !validLevels[args] {
		return c.Send("❌ Invalid level. Use: off, low, medium, high")
	}

	if err := b.store.SetSessionOption(chatID, "think_level", args); err != nil {
		return c.Send("❌ Failed to set think level")
	}
	return c.Send(fmt.Sprintf("✅ Think level set to: `%s`", args),
		&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleVerboseCommand toggles verbose mode
func (b *Bot) handleVerboseCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	args := strings.TrimSpace(c.Message().Payload)

	if args == "" {
		verbose, _ := b.store.GetVerbose(chatID)
		state := "off"
		if verbose {
			state = "on"
		}
		return c.Send(fmt.Sprintf("📝 Verbose: `%s`\n\nOptions: `/verbose on` | `/verbose off`", state),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	switch args {
	case "on":
		b.store.SetVerbose(chatID, true)
		return c.Send("✅ Verbose mode: on")
	case "off":
		b.store.SetVerbose(chatID, false)
		return c.Send("✅ Verbose mode: off")
	default:
		return c.Send("❌ Use: on, off")
	}
}

// handleQueueCommand adjusts queue settings
func (b *Bot) handleQueueCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	args := strings.Fields(c.Message().Payload)

	if len(args) == 0 {
		mode, _ := b.store.GetSessionOption(chatID, "queue_mode")
		if mode == "" {
			mode = "collect"
		}
		debounceMs := b.debouncer.GetDelay()
		return c.Send(fmt.Sprintf("🪢 Queue: `%s` (debounce %dms)\n\nUsage: `/queue <mode> [debounce_ms]`\nModes: collect, steer, interrupt", mode, debounceMs.Milliseconds()),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	mode := args[0]
	validModes := map[string]bool{"collect": true, "steer": true, "interrupt": true}
	if !validModes[mode] {
		return c.Send("❌ Invalid mode. Use: collect, steer, interrupt")
	}

	if err := b.store.SetSessionOption(chatID, "queue_mode", mode); err != nil {
		return c.Send("❌ Failed to set queue mode")
	}

	// Optional debounce ms
	if len(args) > 1 {
		if ms, err := strconv.Atoi(args[1]); err == nil && ms >= 0 && ms <= 10000 {
			b.debouncer.SetDelay(chatID, ms)
		}
	}

	return c.Send(fmt.Sprintf("✅ Queue mode set to: `%s`", mode),
		&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleTTSCommand controls text-to-speech
func (b *Bot) handleTTSCommand(c telebot.Context) error {
	args := strings.TrimSpace(c.Message().Payload)

	if args == "" || args == "help" {
		return c.Send(`🔊 *TTS Commands:*

/tts on — Enable auto-TTS
/tts off — Disable auto-TTS
/tts status — Show TTS settings`, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	switch args {
	case "on":
		return c.Send("✅ TTS enabled (not yet fully implemented)")
	case "off":
		return c.Send("✅ TTS disabled")
	case "status":
		return c.Send("🔊 TTS: off (default)")
	default:
		return c.Send("❌ Unknown TTS action. Use: on, off, status, help")
	}
}

// handleRestartCommand restarts the bot process
func (b *Bot) handleRestartCommand(c telebot.Context) error {
	if !b.authManager.IsAdmin(c.Sender().ID) {
		return c.Send("🔒 This command is only available to administrators.")
	}

	c.Send("🔄 Restarting...")
	log.Println("Restart requested via /restart command")

	// Exit with code 0 — assumes a process manager will restart
	go func() {
		os.Exit(0)
	}()

	return nil
}
