package bot

import (
	"context"
	"fmt"
	"log"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
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
// It spawns a sub-agent as an isolated child session and notifies the parent
// chat with a summary or failure message when the sub-agent finishes.
func (b *Bot) handleTaskCommand(c telebot.Context) error {
	chatID := c.Chat().ID
	payload := strings.TrimSpace(c.Message().Payload)

	req, err := parseTaskArgs(payload)
	if err != nil {
		return c.Send(fmt.Sprintf("❌ Usage: /task <description> [--model <model>] [--thinking off|low|medium|high]\n\nError: %s", err))
	}

	// Resolve model: use request override, then session override, then default
	model := req.Model
	if model == "" {
		model = b.getEffectiveModel(chatID)
	}
	// Resolve alias if set
	model = b.resolveModelAlias(model)

	// Acknowledge immediately so the user knows the task is queued
	thinkNote := ""
	if req.ThinkLevel != "" {
		thinkNote = fmt.Sprintf(" (thinking: %s)", req.ThinkLevel)
	}
	ackMsg := fmt.Sprintf("⚙️ Sub-agent started%s\nModel: `%s`\nTask: %s",
		thinkNote, model, req.Description)
	if err := c.Send(ackMsg, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
		log.Printf("[task] failed to send ack: %v", err)
	}

	// Capture chat reference for the notification goroutine
	chat := c.Chat()

	go func() {
		log.Printf("[task] spawning sub-agent for chat=%d model=%s thinking=%s desc=%.80s",
			chatID, model, req.ThinkLevel, req.Description)

		result := b.runSubagent(req, model)

		var notifText string
		if result.Success {
			notifText = fmt.Sprintf("✅ *Task completed*\n\n%s", result.Summary)
		} else {
			notifText = fmt.Sprintf("❌ *Task failed*\n\n%s", result.Error.Error())
		}

		if _, err := b.api.Send(chat, notifText, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
			log.Printf("[task] failed to send completion notification to chat=%d: %v", chatID, err)
		}
	}()

	return nil
}

// runSubagent creates a fresh ToolCallingAgent and processes the task description.
// It is designed to run in its own goroutine as an isolated child session.
func (b *Bot) runSubagent(req agent.SubagentSpawnRequest, model string) agent.SubagentResult {
	// Build an AI client for the requested model
	var aiClient ai.Client
	if model != b.aiConfig.Model {
		cfg := ai.ProviderConfig{
			Name:    b.aiConfig.Provider,
			APIKey:  b.aiConfig.APIKey,
			Model:   model,
			BaseURL: b.aiConfig.BaseURL,
		}
		var err error
		aiClient, err = ai.NewClient(cfg)
		if err != nil {
			log.Printf("[task] failed to create AI client for model %s: %v", model, err)
			aiClient = b.ai // fall back to default
		}
	} else {
		aiClient = b.ai
	}

	// Get personality from default agent profile
	profile := b.getActiveAgentProfile(0) // 0 = no chat, returns default
	filteredTools := b.getFilteredToolRegistry(profile)

	subAgent := agent.NewToolCallingAgent(aiClient, filteredTools, profile.Personality)
	subAgent.SetModelAliases(b.aiConfig.ModelAliases)
	if req.ThinkLevel != "" {
		subAgent.SetThinkLevel(req.ThinkLevel)
	}

	// Run the sub-agent with a background context (not tied to the HTTP request)
	ctx := context.Background()
	resp, err := subAgent.ProcessRequest(ctx, req.Description, "")
	if err != nil {
		return agent.SubagentResult{Success: false, Error: err}
	}

	summary := resp.Message
	if strings.TrimSpace(summary) == "" {
		summary = "Task completed with no output."
	}

	return agent.SubagentResult{Success: true, Summary: summary}
}
