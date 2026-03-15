package bot

import (
	"context"
	"fmt"
	"log"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/jobs"
	routerpkg "ok-gobot/internal/router"
	"ok-gobot/internal/storage"
)

func (b *Bot) routeAndHandleMessage(
	ctx context.Context,
	c telebot.Context,
	sessionKey agent.SessionKey,
	content string,
	userContent []ai.ContentBlock,
) error {
	if b.router == nil || b.jobService == nil {
		session, _ := b.store.GetSession(c.Chat().ID)
		if len(userContent) > 0 {
			return b.processViaHubWithContent(ctx, c, sessionKey, content, userContent, session)
		}
		return b.processViaHub(ctx, c, sessionKey, content, session)
	}

	decision := b.router.Decide(content)
	switch decision.Action {
	case routerpkg.ActionClarify:
		return b.deliverReplyText(c, decision.Summary)
	case routerpkg.ActionLaunchJob:
		return b.launchBackgroundJob(c, sessionKey, content, decision)
	default:
		return b.processDirectReply(ctx, c, sessionKey, content, userContent)
	}
}

func (b *Bot) processDirectReply(
	ctx context.Context,
	c telebot.Context,
	sessionKey agent.SessionKey,
	content string,
	userContent []ai.ContentBlock,
) error {
	chatID := c.Chat().ID
	profile := b.activeProfile(chatID)
	client := b.getReplyClient(chatID, profile)
	messages := b.buildFastReplyMessages(chatID, sessionKey, content, userContent, profile)

	resp, err := client.CompleteWithTools(ctx, messages, nil)
	if err != nil {
		b.finishAckWithError(chatID, c)
		return err
	}
	if len(resp.Choices) == 0 {
		b.finishAckWithError(chatID, c)
		return fmt.Errorf("empty response from model")
	}

	reply := strings.TrimSpace(resp.Choices[0].Message.Content)
	if reply == "" {
		reply = "⚠️ Got an empty response from the model."
	}

	if resp.Usage != nil {
		b.store.UpdateTokenUsage(chatID, resp.Usage.PromptTokens, resp.Usage.CompletionTokens, resp.Usage.TotalTokens) //nolint:errcheck
	}
	if err := b.store.SaveSession(chatID, reply); err != nil {
		log.Printf("[bot] failed to save session: %v", err)
	}
	if err := b.store.SaveSessionMessagePairV2(string(sessionKey), content, reply); err != nil {
		log.Printf("[bot] failed to save v2 transcript: %v", err)
	}
	if err := b.memory.AppendToToday(fmt.Sprintf("Assistant (%s): %s", profile.Name, reply)); err != nil {
		log.Printf("[bot] failed to append to memory: %v", err)
	}

	return b.deliverReplyText(c, reply)
}

func (b *Bot) launchBackgroundJob(c telebot.Context, sessionKey agent.SessionKey, content string, decision routerpkg.Decision) error {
	chatID := c.Chat().ID
	profile := b.activeProfile(chatID)
	model := b.profileModel(chatID, profile)

	job, err := b.jobService.Launch(context.Background(), jobs.LaunchRequest{
		SessionKey:         string(sessionKey),
		ChatID:             chatID,
		CreatedByMessageID: int64(c.Message().ID),
		TaskType:           "router_job",
		Summary:            decision.Summary,
		RouterDecision:     string(decision.Action),
		WorkerBackend:      decision.WorkerBackend,
		WorkerProfile:      profile.Name,
		Input: jobs.InputPayload{
			Prompt: content,
			Model:  model,
		},
	})
	if err != nil {
		b.finishAckWithError(chatID, c)
		return err
	}

	ackText := fmt.Sprintf("⚙️ Job #%d queued via `%s`\n\n%s", job.ID, job.WorkerBackend, decision.Summary)
	if err := b.deliverReplyText(c, ackText); err != nil {
		return err
	}

	if err := b.store.SaveSessionMessagePairV2(string(sessionKey), content, fmt.Sprintf("[job #%d queued via %s]", job.ID, job.WorkerBackend)); err != nil {
		log.Printf("[bot] failed to save job launch transcript: %v", err)
	}
	return nil
}

func (b *Bot) deliverReplyText(c telebot.Context, msg string) error {
	chatID := c.Chat().ID
	replyTarget := parseReplyTags(msg)
	msg = replyTarget.Clean
	if strings.TrimSpace(msg) == "" {
		msg = "⚠️ Got an empty response from the model."
	}

	const maxTelegramLen = 4096
	chunks := splitMessage(msg, maxTelegramLen)
	ackMsg := b.takeAck(chatID)
	for i, chunk := range chunks {
		if i == 0 && ackMsg != nil {
			if _, err := b.api.Edit(ackMsg, chunk); err != nil {
				if _, sendErr := b.api.Send(c.Chat(), chunk); sendErr != nil {
					return sendErr
				}
			}
			continue
		}

		sendOpts := &telebot.SendOptions{}
		if i == 0 {
			switch {
			case replyTarget.MessageID == -1:
				sendOpts.ReplyTo = c.Message()
			case replyTarget.MessageID > 0:
				sendOpts.ReplyTo = &telebot.Message{ID: replyTarget.MessageID}
			}
		}
		if _, err := b.api.Send(c.Chat(), chunk, sendOpts); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) finishAckWithError(chatID int64, c telebot.Context) {
	if ackMsg := b.takeAck(chatID); ackMsg != nil {
		if _, err := b.api.Edit(ackMsg, "❌ Sorry, I encountered an error processing your request."); err == nil {
			return
		}
	}
	b.api.Send(c.Chat(), "❌ Sorry, I encountered an error processing your request.") //nolint:errcheck
}

func (b *Bot) buildFastReplyMessages(
	chatID int64,
	sessionKey agent.SessionKey,
	content string,
	userContent []ai.ContentBlock,
	profile *agent.AgentProfile,
) []ai.ChatMessage {
	model := b.profileModel(chatID, profile)
	messages := []ai.ChatMessage{
		{
			Role:    ai.RoleSystem,
			Content: b.systemPromptForProfile(profile),
		},
	}

	if v2Msgs, err := b.store.GetSessionMessagesV2(string(sessionKey), 40); err == nil && len(v2Msgs) > 0 {
		history := make([]ai.ChatMessage, 0, len(v2Msgs))
		for _, m := range v2Msgs {
			history = append(history, ai.ChatMessage{Role: m.Role, Content: m.Content})
		}
		history = trimHistoryToTokenBudget(history, model)
		messages = append(messages, history...)
	}

	userMsg := ai.ChatMessage{Role: ai.RoleUser, Content: content}
	if len(userContent) > 0 {
		userMsg.ContentBlocks = userContent
	}
	messages = append(messages, userMsg)

	return messages
}

func (b *Bot) activeProfile(chatID int64) *agent.AgentProfile {
	if b.agentRegistry != nil {
		if activeName, err := b.store.GetActiveAgent(chatID); err == nil && activeName != "" {
			if profile := b.agentRegistry.Get(activeName); profile != nil {
				return profile
			}
		}
		if profile := b.agentRegistry.Default(); profile != nil {
			return profile
		}
	}

	return &agent.AgentProfile{
		Name:        "default",
		Personality: b.personality,
		Model:       b.getEffectiveModel(chatID),
	}
}

func (b *Bot) systemPromptForProfile(profile *agent.AgentProfile) string {
	if profile != nil && profile.Personality != nil {
		return profile.Personality.GetSystemPrompt()
	}
	if b.personality != nil {
		return b.personality.GetSystemPrompt()
	}
	return "You are a concise, helpful assistant."
}

func (b *Bot) profileModel(chatID int64, profile *agent.AgentProfile) string {
	if override, err := b.store.GetModelOverride(chatID); err == nil && strings.TrimSpace(override) != "" {
		return override
	}
	if profile != nil && strings.TrimSpace(profile.Model) != "" {
		return profile.Model
	}
	return b.aiConfig.Model
}

func (b *Bot) getReplyClient(chatID int64, profile *agent.AgentProfile) ai.Client {
	model := b.profileModel(chatID, profile)
	provider := b.aiConfig.Provider
	baseURL := b.aiConfig.BaseURL
	apiKey := b.aiConfig.APIKey

	if profile != nil {
		if strings.TrimSpace(profile.Provider) != "" {
			provider = profile.Provider
		}
		if strings.TrimSpace(profile.BaseURL) != "" {
			baseURL = profile.BaseURL
		}
		if strings.TrimSpace(profile.APIKey) != "" {
			apiKey = profile.APIKey
		}
	}

	if provider == b.aiConfig.Provider && baseURL == b.aiConfig.BaseURL && apiKey == b.aiConfig.APIKey && model == b.aiConfig.Model && b.ai != nil {
		return b.ai
	}

	client, err := ai.NewClientWithDroid(ai.ProviderConfig{
		Name:    provider,
		APIKey:  apiKey,
		BaseURL: baseURL,
		Model:   model,
	}, b.aiConfig.Droid)
	if err != nil {
		log.Printf("[bot] failed to build reply client for provider=%s model=%s: %v", provider, model, err)
		if b.ai != nil {
			return b.ai
		}
		return client
	}
	return client
}

// HandleJobUpdate delivers meaningful terminal job updates back to Telegram.
func (b *Bot) HandleJobUpdate(_ context.Context, job *storage.JobRecord, update jobs.Update) {
	if job == nil {
		return
	}

	var text string
	switch update.Status {
	case storage.JobDone:
		resultText := "Task completed with no output."
		if update.Result != nil && strings.TrimSpace(update.Result.Output) != "" {
			resultText = update.Result.Output
		}
		text = fmt.Sprintf("✅ Job #%d completed\n\n%s", job.ID, resultText)
	case storage.JobFailed:
		text = fmt.Sprintf("❌ Job #%d failed\n\n%s", job.ID, update.Message)
	case storage.JobCancelled:
		text = fmt.Sprintf("🛑 Job #%d cancelled", job.ID)
	default:
		return
	}

	chat := &telebot.Chat{ID: job.ChatID}
	for _, chunk := range splitMessage(text, 4096) {
		if _, err := b.api.Send(chat, chunk, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
			log.Printf("[bot] failed to deliver job update for job=%d: %v", job.ID, err)
			return
		}
	}
}
