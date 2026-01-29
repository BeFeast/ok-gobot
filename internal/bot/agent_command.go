package bot

import (
	"fmt"
	"log"
	"strings"

	"gopkg.in/telebot.v4"
)

// handleAgentCommand handles the /agent command
func (b *Bot) handleAgentCommand(c telebot.Context) error {
	// Skip if no agent registry configured
	if b.agentRegistry == nil {
		return c.Send("⚠️ Multi-agent system not configured")
	}

	args := strings.TrimSpace(c.Message().Payload)
	chatID := c.Chat().ID

	// No arguments - show current agent
	if args == "" {
		currentAgent, err := b.store.GetActiveAgent(chatID)
		if err != nil {
			log.Printf("Failed to get active agent: %v", err)
			return c.Send("❌ Failed to get current agent")
		}

		profile := b.agentRegistry.Get(currentAgent)
		if profile == nil {
			return c.Send(fmt.Sprintf("⚠️ Current agent '%s' not found", currentAgent))
		}

		var response strings.Builder
		response.WriteString(fmt.Sprintf("🤖 *Current Agent:* `%s`\n\n", profile.Name))
		response.WriteString(fmt.Sprintf("Model: `%s`\n", profile.Model))

		if profile.HasToolRestrictions() {
			response.WriteString(fmt.Sprintf("Allowed tools: %s\n", strings.Join(profile.AllowedTools, ", ")))
		} else {
			response.WriteString("Allowed tools: all\n")
		}

		response.WriteString("\n💡 Use `/agent list` to see all available agents")
		response.WriteString("\n💡 Use `/agent <name>` to switch agent")

		return c.Send(response.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	// Handle "list" command
	if args == "list" {
		agents := b.agentRegistry.List()

		var response strings.Builder
		response.WriteString("🤖 *Available Agents:*\n\n")

		for _, name := range agents {
			profile := b.agentRegistry.Get(name)
			if profile == nil {
				continue
			}

			response.WriteString(fmt.Sprintf("• `%s`", name))
			if profile.Model != "" {
				response.WriteString(fmt.Sprintf(" (model: %s)", profile.Model))
			}
			response.WriteString("\n")
		}

		response.WriteString("\nUsage: `/agent <name>` to switch agent")

		return c.Send(response.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	// Switch to specified agent
	agentName := args
	profile := b.agentRegistry.Get(agentName)
	if profile == nil {
		return c.Send(fmt.Sprintf("❌ Agent '%s' not found. Use `/agent list` to see available agents.", agentName))
	}

	if err := b.store.SetActiveAgent(chatID, agentName); err != nil {
		log.Printf("Failed to set active agent: %v", err)
		return c.Send("❌ Failed to switch agent")
	}

	return c.Send(fmt.Sprintf("✅ Switched to agent: `%s`\n\nModel: `%s`",
		profile.Name, profile.Model),
		&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}
