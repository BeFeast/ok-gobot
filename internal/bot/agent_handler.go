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

// createAgentToolAgent creates a ToolCallingAgent for the active agent profile
func (b *Bot) createAgentToolAgent(profile *agent.AgentProfile) *agent.ToolCallingAgent {
	filteredTools := b.getFilteredToolRegistry(profile)
	return agent.NewToolCallingAgent(b.ai, filteredTools, profile.Personality)
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

// handleAgentRequestWithProfile processes request using the active agent profile
func (b *Bot) handleAgentRequestWithProfile(ctx context.Context, c telebot.Context, content, session string) error {
	chatID := c.Chat().ID

	// Get active agent profile
	profile := b.getActiveAgentProfile(chatID)

	// Create tool agent for this profile
	toolAgent := b.createAgentToolAgent(profile)

	// Get effective model
	model := b.getAgentModel(chatID, profile)

	// Start typing indicator
	stopTyping := NewTypingIndicator(b.api, c.Chat())
	defer stopTyping()

	// Process request
	response, err := toolAgent.ProcessRequest(ctx, content, session)
	if err != nil {
		log.Printf("Agent error: %v", err)
		return c.Send("❌ Sorry, I encountered an error processing your request.")
	}

	// If a tool was used, show intermediate message
	if response.ToolUsed {
		b.api.Send(c.Chat(), fmt.Sprintf("🔧 Using %s tool...", response.ToolName))
	}

	// Send final response
	if err := c.Send(response.Message); err != nil {
		return err
	}

	// Save to memory
	memoryEntry := fmt.Sprintf("Assistant (%s): %s", profile.Name, response.Message)
	if response.ToolUsed {
		memoryEntry += fmt.Sprintf(" [Tool: %s]", response.ToolName)
	}
	if err := b.memory.AppendToToday(memoryEntry); err != nil {
		log.Printf("Failed to save to memory: %v", err)
	}

	// Save session
	if err := b.store.SaveSession(chatID, response.Message); err != nil {
		log.Printf("Failed to save session: %v", err)
	}

	log.Printf("Agent '%s' (model: %s) processed request", profile.Name, model)

	return nil
}

// handleStreamingRequestWithProfile processes message with streaming response using active agent
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

	// Send initial "thinking" message
	thinkingMsg, err := b.api.Send(c.Chat(), "💭 Thinking...")
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
