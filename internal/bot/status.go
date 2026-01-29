package bot

import (
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
)

var startTime = time.Now()

// handleStatusCommand shows rich bot status
func (b *Bot) handleStatusCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	name := b.personality.GetName()
	emoji := b.personality.GetEmoji()

	var sb strings.Builder

	// Header with version and git commit
	version := "0.1.0"
	commit := getGitCommit()
	if commit != "" {
		sb.WriteString(fmt.Sprintf("🦞 *%s* %s (%s)\n", name, version, commit))
	} else {
		sb.WriteString(fmt.Sprintf("🦞 *%s* %s %s\n", name, version, emoji))
	}

	// Model and provider
	if b.aiConfig.APIKey != "" {
		maskedKey := maskAPIKey(b.aiConfig.APIKey)
		sb.WriteString(fmt.Sprintf("🧠 Model: `%s` · 🔑 %s (%s)\n", b.aiConfig.Model, maskedKey, b.aiConfig.Provider))
	} else {
		sb.WriteString("⚠️ AI not configured\n")
	}

	// Token usage
	usage, err := b.store.GetTokenUsage(chatID)
	if err != nil {
		log.Printf("Failed to get token usage: %v", err)
	}
	if usage != nil && (usage.InputTokens > 0 || usage.OutputTokens > 0) {
		sb.WriteString(fmt.Sprintf("🧮 Tokens: %s in / %s out\n",
			formatTokenCount(usage.InputTokens), formatTokenCount(usage.OutputTokens)))
	}

	// Context window
	contextLimit := agent.ModelLimits(b.aiConfig.Model)
	if usage != nil && usage.TotalTokens > 0 {
		pct := float64(usage.TotalTokens) / float64(contextLimit) * 100
		sb.WriteString(fmt.Sprintf("📚 Context: %s/%s (%.0f%%) · 🧹 Compactions: %d\n",
			formatTokenCount(usage.TotalTokens), formatTokenCount(contextLimit), pct, usage.CompactionCount))
	} else {
		sb.WriteString(fmt.Sprintf("📚 Context: 0/%s · 🧹 Compactions: 0\n", formatTokenCount(contextLimit)))
	}

	// Session info
	if usage != nil && usage.UpdatedAt != "" {
		ago := formatTimeAgo(usage.UpdatedAt)
		activeAgent, _ := b.store.GetActiveAgent(chatID)
		sb.WriteString(fmt.Sprintf("🧵 Session: `%s` · updated %s\n", activeAgent, ago))
	}

	// Runtime options
	thinkLevel, _ := b.store.GetSessionOption(chatID, "think_level")
	if thinkLevel == "" {
		thinkLevel = "default"
	}
	queueMode, _ := b.store.GetSessionOption(chatID, "queue_mode")
	if queueMode == "" {
		queueMode = "collect"
	}
	queueDepth := b.debouncer.GetPendingCount()
	sb.WriteString(fmt.Sprintf("⚙️ Think: %s · 🪢 Queue: %s (depth %d)\n", thinkLevel, queueMode, queueDepth))

	// Uptime
	uptime := time.Since(startTime)
	sb.WriteString(fmt.Sprintf("\n🟢 Running for %s", formatDuration(uptime)))

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

func getGitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func maskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:6] + "…" + key[len(key)-4:]
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
