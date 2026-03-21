package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/tools"
	"ok-gobot/internal/version"
)

var startTime = time.Now()

// handleStatusCommand shows rich bot status
func (b *Bot) handleStatusCommand(c telebot.Context) error {
	return c.Send(b.buildStatusString(c.Chat().ID), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// buildStatusString builds the full status string for a given chatID.
// Pass chatID=-1 for TUI (no per-chat session data).
func (b *Bot) buildStatusString(chatID int64) string {
	name := b.personality.GetName()
	emoji := b.personality.GetEmoji()

	var sb strings.Builder

	// Header with version
	sb.WriteString(fmt.Sprintf("🦞 *%s* %s %s\n", name, version.String(), emoji))

	// Model and provider
	if b.aiConfig.APIKey != "" {
		maskedKey := strings.ReplaceAll(maskAPIKey(b.aiConfig.APIKey), "_", "\\_")
		sb.WriteString(fmt.Sprintf("🧠 Model: `%s` · 🔑 %s (%s)\n", b.aiConfig.Model, maskedKey, b.aiConfig.Provider))
	} else {
		sb.WriteString("⚠️ AI not configured\n")
	}

	// Context window
	contextLimit := agent.ModelLimits(b.aiConfig.Model)
	if chatID >= 0 {
		usage, err := b.store.GetTokenUsage(chatID)
		if err != nil {
			log.Printf("Failed to get token usage: %v", err)
		}
		if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
			sb.WriteString(fmt.Sprintf("🧮 Tokens: %s in / %s out\n",
				formatTokenCount(usage.InputTokens), formatTokenCount(usage.OutputTokens)))
		}
		if usage != nil && usage.TotalTokens > 0 {
			pct := float64(usage.TotalTokens) / float64(contextLimit) * 100
			sb.WriteString(fmt.Sprintf("📚 Context: %s/%s (%.0f%%) · 🧹 Compactions: %d\n",
				formatTokenCount(usage.TotalTokens), formatTokenCount(contextLimit), pct, usage.CompactionCount))
		} else {
			sb.WriteString(fmt.Sprintf("📚 Context: 0/%s (0%%) · 🧹 Compactions: 0\n", formatTokenCount(contextLimit)))
		}
		if usage != nil && usage.UpdatedAt != "" {
			ago := formatTimeAgo(usage.UpdatedAt)
			activeAgent, _ := b.store.GetActiveAgent(chatID)
			sb.WriteString(fmt.Sprintf("🧵 Session: `%s` · updated %s\n", activeAgent, ago))
		}
	} else {
		// TUI: show global context limit, no per-session data
		sb.WriteString(fmt.Sprintf("📚 Context limit: %s · 🧵 Session: tui\n", formatTokenCount(contextLimit)))
	}

	// Runtime options
	thinkLevel := "off (default)"
	queueMode := "interrupt"
	if chatID >= 0 {
		if v, _ := b.store.GetSessionOption(chatID, "think_level"); v != "" {
			thinkLevel = v
		}
		if v, _ := b.store.GetSessionOption(chatID, "queue_mode"); v != "" {
			queueMode = v
		}
	}
	queueDepth := b.debouncer.GetPendingCount()
	sb.WriteString(fmt.Sprintf("⚙️ Think: %s · 🪢 Queue: %s (depth %d)\n", thinkLevel, queueMode, queueDepth))

	// Estop state
	estopEnabled, err := b.store.IsEmergencyStopEnabled()
	if err != nil {
		log.Printf("Failed to load estop state: %v", err)
	}
	if estopEnabled {
		families := strings.Join(tools.DangerousToolFamilies(), ", ")
		sb.WriteString(fmt.Sprintf("🛑 estop: ON — blocked: %s\n", families))
	} else {
		sb.WriteString("✅ estop: off\n")
	}

	// Uptime
	uptime := time.Since(startTime)
	sb.WriteString(fmt.Sprintf("\n🟢 Running for %s", formatDuration(uptime)))

	return sb.String()
}

func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:6] + "..." + key[len(key)-4:]
}

func formatTimeAgo(timestamp string) string {
	t, err := time.Parse("2006-01-02 15:04:05", timestamp)
	if err != nil {
		return timestamp
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd %dh", days, hours)
}
