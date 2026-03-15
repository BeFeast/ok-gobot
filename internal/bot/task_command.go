package bot

import (
	"context"
	"fmt"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/jobs"
)

type taskArgs struct {
	AgentName   string
	Model       string
	ThinkLevel  string
	Description string
}

// parseTaskArgs parses the /job or /task payload.
// Syntax: <description> [--agent <profile>] [--model <model>] [--thinking <level>]
func parseTaskArgs(payload string) (taskArgs, error) {
	var req taskArgs

	if strings.TrimSpace(payload) == "" {
		return req, fmt.Errorf("no task description provided")
	}

	words := strings.Fields(payload)
	var descWords []string

	for i := 0; i < len(words); i++ {
		switch words[i] {
		case "--agent":
			if i+1 >= len(words) {
				return req, fmt.Errorf("--agent requires a value")
			}
			i++
			req.AgentName = words[i]
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

// handleTaskCommand handles the /job command and the legacy /task alias.
func (b *Bot) handleTaskCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	payload := strings.TrimSpace(c.Message().Payload)

	req, err := parseTaskArgs(payload)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ Usage: /job <description> [--agent <profile>] [--model <model>] [--thinking off|low|medium|high]\n\nError: %s", err))
	}
	if b.jobService == nil {
		return c.Send("❌ Background jobs are not configured")
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
	agentNote := req.AgentName
	if agentNote == "" {
		agentNote = "(session default)"
	}
	displayModel := model
	if displayModel == "" {
		displayModel = "(session default)"
	}
	decision := b.router.Decide(req.Description)

	job, err := b.jobService.Launch(context.Background(), jobs.LaunchRequest{
		SessionKey:         string(sessionKeyForChat(c.Chat())),
		ChatID:             chatID,
		CreatedByMessageID: int64(c.Message().ID),
		TaskType:           "operator_job",
		Summary:            decision.Summary,
		RouterDecision:     "explicit_job_command",
		WorkerBackend:      decision.WorkerBackend,
		WorkerProfile:      req.AgentName,
		Input: jobs.InputPayload{
			Prompt: req.Description,
			Model:  model,
		},
	})
	if err != nil {
		return c.Send(fmt.Sprintf("❌ Failed to launch job: %s", err))
	}

	ackText := fmt.Sprintf("⚙️ Job #%d started%s\nWorker: `%s`\nAgent: `%s`\nModel: `%s`\nTask: %s",
		job.ID, thinkNote, job.WorkerBackend, agentNote, displayModel, req.Description)
	return c.Send(ackText, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}
