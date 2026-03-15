package bot

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/delegation"
)

// parseTaskArgs parses the /task command payload into a SubagentSpawnRequest.
// Syntax: <description> [--model <model>] [--thinking <level>] [--max-tools <n>]
// [--max-duration <duration>] [--output <text|markdown|json>] [--schema <shape>]
// [--memory <inherit|read_only|allow_writes>]
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
		case "--max-tools":
			if i+1 >= len(words) {
				return req, fmt.Errorf("--max-tools requires a value")
			}
			i++
			n, err := strconv.Atoi(words[i])
			if err != nil || n <= 0 {
				return req, fmt.Errorf("--max-tools must be a positive integer")
			}
			req.MaxToolCalls = n
		case "--max-duration":
			if i+1 >= len(words) {
				return req, fmt.Errorf("--max-duration requires a value")
			}
			i++
			d, err := time.ParseDuration(words[i])
			if err != nil || d <= 0 {
				return req, fmt.Errorf("--max-duration must be a valid positive duration")
			}
			req.MaxDuration = d
		case "--output":
			if i+1 >= len(words) {
				return req, fmt.Errorf("--output requires a value")
			}
			i++
			format, ok := delegation.ParseOutputFormat(words[i])
			if !ok {
				return req, fmt.Errorf("--output must be one of: text, markdown, json")
			}
			req.OutputFormat = format
		case "--schema":
			if i+1 >= len(words) {
				return req, fmt.Errorf("--schema requires a value")
			}
			i++
			req.OutputSchema = words[i]
		case "--memory":
			if i+1 >= len(words) {
				return req, fmt.Errorf("--memory requires a value")
			}
			i++
			policy, ok := delegation.ParseMemoryPolicy(words[i])
			if !ok {
				return req, fmt.Errorf("--memory must be one of: inherit, read_only, allow_writes")
			}
			req.MemoryPolicy = policy
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
// It still depends on the legacy RuntimeHub compatibility path; keep new
// feature work off this surface while the chat/jobs runtime takes over.
func (b *Bot) handleTaskCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	payload := strings.TrimSpace(c.Message().Payload)

	req, err := parseTaskArgs(payload)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ Usage: /task <description> [--model <model>] [--thinking off|low|medium|high] [--max-tools <n>] [--max-duration <duration>] [--output text|markdown|json] [--schema <shape>] [--memory inherit|read_only|allow_writes]\n\nError: %s", err))
	}

	// Resolve model alias if set.
	model := req.Model
	if model != "" {
		model = b.resolveModelAlias(model)
	}
	job := req.Job()
	job.Model = model

	// Acknowledge immediately so the user knows the task is queued.
	thinkNote := ""
	if req.ThinkLevel != "" {
		thinkNote = fmt.Sprintf(" (thinking: %s)", req.ThinkLevel)
	}
	displayModel := model
	if displayModel == "" {
		displayModel = "(session default)"
	}
	ackText := fmt.Sprintf("⚙️ Sub-agent started%s\nModel: `%s`\nBudget: `%d tools / %s`\nOutput: `%s`\nMemory: `%s`\nTask: %s",
		thinkNote, displayModel, job.MaxToolCalls, job.MaxDuration, job.OutputFormat, job.MemoryPolicy, req.Description)
	if err := c.Send(ackText, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
		log.Printf("[task] failed to send ack: %v", err)
	}

	// Capture chat reference for the notification goroutine.
	chat := c.Chat()

	req.Model = model
	b.startTaskRun(chat, chatID, req, taskCommandNotifications)

	return nil
}
