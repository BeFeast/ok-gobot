package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

// Bot wraps the Telegram bot with business logic
type Bot struct {
	api         *telebot.Bot
	store       *storage.Store
	ai          ai.Client
	aiConfig    AIConfig
	personality *agent.Personality
	safety      *agent.Safety
	memory      *agent.Memory
	toolAgent   *agent.ToolCallingAgent
	adminID     int64
}

// AIConfig holds AI configuration for status display
type AIConfig struct {
	Provider string
	Model    string
	APIKey   string
}

// New creates a new bot instance
func New(token string, store *storage.Store, aiClient ai.Client, aiCfg AIConfig, personality *agent.Personality) (*Bot, error) {
	pref := telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}

	api, err := telebot.NewBot(pref)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	// Create tool registry
	toolRegistry, _ := tools.LoadFromConfig("")

	return &Bot{
		api:         api,
		store:       store,
		ai:          aiClient,
		aiConfig:    aiCfg,
		personality: personality,
		safety:      agent.NewSafety(),
		memory:      agent.NewMemory(""),
		toolAgent:   agent.NewToolCallingAgent(aiClient, toolRegistry, personality),
	}, nil
}

// Start begins processing updates
func (b *Bot) Start(ctx context.Context) error {
	// Handle text messages
	b.api.Handle(telebot.OnText, func(c telebot.Context) error {
		return b.handleMessage(ctx, c)
	})

	// Handle commands
	b.api.Handle("/start", func(c telebot.Context) error {
		greeting := fmt.Sprintf("🦞 Welcome! I'm %s %s\n\n%s",
			b.personality.Name,
			b.personality.Emoji,
			"I'm your personal AI assistant. Just send me a message and I'll help you out.")
		return c.Send(greeting)
	})

	b.api.Handle("/help", func(c telebot.Context) error {
		help := fmt.Sprintf(`🦞 %s Commands:

/start - Start the bot
/help - Show this help
/status - Check bot status
/clear - Clear conversation history
/memory - Show today's memory
/tools - List available tools`, b.personality.Name)
		return c.Send(help)
	})

	b.api.Handle("/status", func(c telebot.Context) error {
		status := fmt.Sprintf("🦞 *%s* (Go Edition) v0.1.0 %s\n\n",
			b.personality.Name,
			b.personality.Emoji)

		if b.aiConfig.APIKey != "" {
			status += fmt.Sprintf("🧠 Model: `%s`\n", b.aiConfig.Model)
			status += fmt.Sprintf("🔑 Provider: `%s`\n", b.aiConfig.Provider)
		} else {
			status += "⚠️ AI not configured\n"
		}

		if b.personality.User != nil && b.personality.User.Name != "" {
			status += fmt.Sprintf("\n👤 Helping: %s\n", b.personality.User.Name)
		}

		status += "\n🟢 Bot is running and ready!"

		return c.Send(status, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	})

	b.api.Handle("/tools", func(c telebot.Context) error {
		tools := b.toolAgent.GetAvailableTools()
		return c.Send(fmt.Sprintf("🔧 Available Tools:\n\n%s", strings.Join(tools, "\n")))
	})

	b.api.Handle("/clear", func(c telebot.Context) error {
		if err := b.store.SaveSession(c.Chat().ID, ""); err != nil {
			return c.Send("❌ Failed to clear history")
		}
		return c.Send("✅ Conversation history cleared")
	})

	b.api.Handle("/memory", func(c telebot.Context) error {
		note, err := b.memory.GetTodayNote()
		if err != nil {
			return c.Send("❌ Failed to load memory")
		}

		if note.Content == "" {
			return c.Send("📓 No entries for today yet")
		}

		return c.Send(fmt.Sprintf("📓 *Today's Memory*\n\n%s", note.Content),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	})

	// Start bot in goroutine
	go b.api.Start()

	// Start heartbeat in background
	go b.startHeartbeat()

	log.Printf("🦞 %s started %s", b.personality.Name, b.personality.Emoji)

	// Wait for context cancellation
	<-ctx.Done()

	log.Println("Stopping bot...")
	b.api.Stop()
	return nil
}

// handleMessage processes incoming messages
func (b *Bot) handleMessage(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chatID := msg.Chat.ID
	userID := msg.Sender.ID
	username := msg.Sender.Username
	content := msg.Text

	// Check for stop phrase first
	if b.safety.IsStopPhrase(content) {
		return c.Send(agent.GetStopPhraseResponse())
	}

	// Log message
	if err := b.store.SaveMessage(chatID, int64(msg.ID), userID, username, content); err != nil {
		log.Printf("Failed to save message: %v", err)
	}

	// Append to daily memory
	if err := b.memory.AppendToToday(fmt.Sprintf("User: %s", content)); err != nil {
		log.Printf("Failed to append to memory: %v", err)
	}

	// Handle special commands
	if strings.HasPrefix(content, "/") {
		return nil // Commands handled separately
	}

	// Get or create session
	session, err := b.store.GetSession(chatID)
	if err != nil {
		log.Printf("Failed to get session: %v", err)
	}

	// Use ToolCallingAgent to process the request
	if b.ai != nil && !strings.HasPrefix(content, "/") {
		return b.handleAgentRequest(ctx, c, content, session)
	}

	// Simple echo response for now
	return c.Send(fmt.Sprintf("You said: %s", content))
}

// handleAgentRequest processes message through the ToolCallingAgent
func (b *Bot) handleAgentRequest(ctx context.Context, c telebot.Context, content, session string) error {
	// Send typing indicator
	b.api.Notify(c.Chat(), telebot.Typing)

	// Process through agent (which may invoke tools)
	response, err := b.toolAgent.ProcessRequest(ctx, content, session)
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
	memoryEntry := fmt.Sprintf("Assistant: %s", response.Message)
	if response.ToolUsed {
		memoryEntry += fmt.Sprintf(" [Tool: %s]", response.ToolName)
	}
	if err := b.memory.AppendToToday(memoryEntry); err != nil {
		log.Printf("Failed to save to memory: %v", err)
	}

	// Save session
	if err := b.store.SaveSession(c.Chat().ID, response.Message); err != nil {
		log.Printf("Failed to save session: %v", err)
	}

	return nil
}
