package bot

import (
	"fmt"
	"log"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
)

// parseTaskArgs parses the /task command payload into a SubagentSpawnRequest.
// Syntax: <description> [--model <model>] [--thinking <level>]
func parseTaskArgs(payload string) (agent.SubagentSpawnRequest, error) {
	var req agent.SubagentSpawnRequest

	if strings.TrimSpace(payload) == "" {
		return req, fmt.Errorf("no task description provided")
	}

	words := strings.Fields(payload)
	var descWords []string

	for i := 0; i < len(words); i++ {
		switch words[i] {
		case "--model":
			if i+1 >= len(words) {
				return req, fmt.Errorf("--model requires a value")
			}
			i++
			req.Model = words[i]
		case "--thinking":
			if i+1 >= len(words) {
				return req, fmt.Errorf("--thinking requires a value")
			}
			i++
			level := words[i]
			validLevels := map[string]bool{"off": true, "low": true, "medium": true, "high": true}
			if !validLevels[level] {
				return req, fmt.Errorf("--thinking must be one of: off, low, medium, high")
			}
			req.ThinkLevel = level
		default:
			descWords = append(descWords, words[i])
		}
	}

	req.Description = strings.Join(descWords, " ")
	if req.Description == "" {
		return req, fmt.Errorf("no task description provided")
	}

	return req, nil
}

// handleTaskCommand handles the /task command.
// It spawns a sub-agent as an isolated child session via the RuntimeHub and
// notifies the parent chat with a summary or failure message when it finishes.
func (b *Bot) handleTaskCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	payload := strings.TrimSpace(c.Message().Payload)

	req, err := parseTaskArgs(payload)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ Usage: /task <description> [--model <model>] [--thinking off|low|medium|high]\n\nError: %s", err))
	}

	// Resolve model alias if set.
	model := req.Model
	if model != "" {
		model = b.resolveModelAlias(model)
	}

	// Acknowledge immediately so the user knows the task is queued.
	thinkNote := ""
	if req.ThinkLevel != "" {
		thinkNote = fmt.Sprintf(" (thinking: %s)", req.ThinkLevel)
	}
	displayModel := model
	if displayModel == "" {
		displayModel = "(session default)"
	}
	ackText := fmt.Sprintf("⚙️ Sub-agent started%s\nModel: `%s`\nTask: %s",
		thinkNote, displayModel, req.Description)
	if err := c.Send(ackText, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
		log.Printf("[task] failed to send ack: %v", err)
	}

	// Capture chat reference for the notification goroutine.
	chat := c.Chat()

	req.Model = model
	b.startTaskRun(chat, chatID, req, taskCommandNotifications)

	return nil
}
