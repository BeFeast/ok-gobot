package bot

import (
	"context"
	"fmt"
	"log"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"

	"gopkg.in/telebot.v4"
)

// getActiveAgentProfile retrieves the active agent profile for a chat
func (b *Bot) getActiveAgentProfile(chatID int64) *agent.AgentProfile {
	// If no agent registry, use default personality
	if b.agentRegistry == nil {
		return &agent.AgentProfile{
			Name:         "default",
			Personality:  b.personality,
			Model:        b.aiConfig.Model,
			AllowedTools: []string{}, // All tools allowed
		}
	}

	// Get active agent name from storage
	agentName, err := b.store.GetActiveAgent(chatID)
	if err != nil {
		log.Printf("Failed to get active agent: %v", err)
		return b.agentRegistry.Default()
	}

	// Get agent profile
	profile := b.agentRegistry.Get(agentName)
	if profile == nil {
		log.Printf("Agent '%s' not found, using default", agentName)
		return b.agentRegistry.Default()
	}

	return profile
}

// getFilteredToolRegistry returns a tool registry filtered by agent's allowed tools
func (b *Bot) getFilteredToolRegistry(profile *agent.AgentProfile) *tools.Registry {
	if !profile.HasToolRestrictions() {
		return b.toolRegistry
	}

	// Create a new filtered registry
	filteredRegistry := tools.NewRegistry()

	for _, tool := range b.toolRegistry.List() {
		if profile.IsToolAllowed(tool.Name()) {
			filteredRegistry.Register(tool)
		}
	}

	return filteredRegistry
}

// getFilteredToolRegistryForChat returns a tool registry for a specific chat,
// replacing the global cron tool with a chat-specific instance so scheduled
// jobs are associated with the correct chatID.
func (b *Bot) getFilteredToolRegistryForChat(chatID int64, profile *agent.AgentProfile) *tools.Registry {
	if b.scheduler == nil {
		return b.getFilteredToolRegistry(profile)
	}

	// Copy all tools from the filtered registry, skipping the global cron tool.
	// We'll inject a fresh per-chat cron tool instead.
	base := b.getFilteredToolRegistry(profile)
	chatRegistry := tools.NewRegistry()
	for _, tool := range base.List() {
		if tool.Name() != "cron" {
			chatRegistry.Register(tool)
		}
	}
	chatRegistry.Register(tools.NewCronTool(b.scheduler, chatID))
	return chatRegistry
}

// createAgentToolAgent creates a ToolCallingAgent for the active agent profile
// using a per-chat tool registry so the cron tool carries the correct chatID,
// and the provided AI client so model overrides are respected.
func (b *Bot) createAgentToolAgent(chatID int64, profile *agent.AgentProfile, aiClient ai.Client) *agent.ToolCallingAgent {
	filteredTools := b.getFilteredToolRegistryForChat(chatID, profile)
	return newToolAgentWithAliases(aiClient, filteredTools, profile.Personality, b.aiConfig.ModelAliases)
}

// getAgentModel returns the model to use for the active agent, considering overrides
func (b *Bot) getAgentModel(chatID int64, profile *agent.AgentProfile) string {
	// Check for session model override first
	override, err := b.store.GetModelOverride(chatID)
	if err == nil && override != "" {
		return override
	}

	// Use agent's configured model
	if profile.Model != "" {
		return profile.Model
	}

	// Fallback to global model
	return b.aiConfig.Model
}

// handleStreamingRequestWithProfile processes message with streaming response using active agent.
// NOTE: This function is not used in the main message path (tool calling is always used instead).
// It is retained for potential future use.
func (b *Bot) handleStreamingRequestWithProfile(ctx context.Context, c telebot.Context, content, session string) error {
	chatID := c.Chat().ID

	// Get active agent profile
	profile := b.getActiveAgentProfile(chatID)

	// Build messages for AI
	messages := []ai.Message{
		{Role: "system", Content: profile.Personality.GetSystemPrompt()},
	}
	if session != "" {
		messages = append(messages, ai.Message{Role: "assistant", Content: session})
	}
	messages = append(messages, ai.Message{Role: "user", Content: content})

	// Send initial placeholder that will be edited as tokens arrive
	thinkingMsg, err := b.api.Send(c.Chat(), "⏳")
	if err != nil {
		return err
	}

	// Create stream editor
	editor := NewStreamEditor(b.api, c.Chat(), thinkingMsg)

	// Start streaming
	streamCh := b.streamingAI.CompleteStream(ctx, messages)

	for chunk := range streamCh {
		if chunk.Error != nil {
			log.Printf("Stream error: %v", chunk.Error)
			b.api.Edit(thinkingMsg, "❌ Sorry, I encountered an error.")
			return chunk.Error
		}

		if chunk.Content != "" {
			editor.Append(chunk.Content)
		}

		if chunk.Done {
			break
		}
	}

	// Final update
	finalContent := editor.Finish()

	// Save to memory
	if err := b.memory.AppendToToday(fmt.Sprintf("Assistant (%s): %s", profile.Name, finalContent)); err != nil {
		log.Printf("Failed to save to memory: %v", err)
	}

	// Save session
	if err := b.store.SaveSession(chatID, finalContent); err != nil {
		log.Printf("Failed to save session: %v", err)
	}

	return nil
}
