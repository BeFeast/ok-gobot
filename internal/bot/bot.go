package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"moltbot/internal/ai"
	"moltbot/internal/storage"
)

// Bot wraps the Telegram bot with business logic
type Bot struct {
	api      *telebot.Bot
	store    *storage.Store
	ai       ai.Client
	aiConfig AIConfig
	adminID  int64
}

// AIConfig holds AI configuration for status display
type AIConfig struct {
	Provider string
	Model    string
	APIKey   string
}

// New creates a new bot instance
func New(token string, store *storage.Store, aiClient ai.Client, aiCfg AIConfig) (*Bot, error) {
	pref := telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}

	api, err := telebot.NewBot(pref)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	return &Bot{
		api:      api,
		store:    store,
		ai:       aiClient,
		aiConfig: aiCfg,
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
		return c.Send("🦞 Welcome to Moltbot!\n\nI'm your personal AI assistant. Just send me a message and I'll help you out.")
	})

	b.api.Handle("/help", func(c telebot.Context) error {
		help := `🦞 Moltbot Commands:

/start - Start the bot
/help - Show this help
/status - Check bot status
/clear - Clear conversation history`
		return c.Send(help)
	})

	b.api.Handle("/status", func(c telebot.Context) error {
		status := "🦞 *ok-gobot* (Go Edition) v0.1.0\n\n"

		if b.aiConfig.APIKey != "" {
			status += fmt.Sprintf("🧠 Model: `%s`\n", b.aiConfig.Model)
			status += fmt.Sprintf("🔑 Provider: `%s`\n", b.aiConfig.Provider)
		} else {
			status += "⚠️ AI not configured\n"
		}

		status += "\n🟢 Bot is running and ready!"

		return c.Send(status, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	})

	b.api.Handle("/clear", func(c telebot.Context) error {
		if err := b.store.SaveSession(c.Chat().ID, ""); err != nil {
			return c.Send("❌ Failed to clear history")
		}
		return c.Send("✅ Conversation history cleared")
	})

	// Start bot in goroutine
	go b.api.Start()

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

	// Log message
	if err := b.store.SaveMessage(chatID, int64(msg.ID), userID, username, content); err != nil {
		log.Printf("Failed to save message: %v", err)
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

	// If AI is configured, process with AI
	if b.ai != nil && !strings.HasPrefix(content, "/") {
		return b.handleAIRequest(ctx, c, content, session)
	}

	// Simple echo response for now
	return c.Send(fmt.Sprintf("You said: %s", content))
}

// handleAIRequest processes message through AI
func (b *Bot) handleAIRequest(ctx context.Context, c telebot.Context, content, session string) error {
	// Send typing indicator
	b.api.Notify(c.Chat(), telebot.Typing)

	// Prepare messages
	messages := []ai.Message{
		{Role: "system", Content: "You are a helpful AI assistant."},
	}

	// Add session history if exists
	if session != "" {
		messages = append(messages, ai.Message{Role: "assistant", Content: session})
	}

	messages = append(messages, ai.Message{Role: "user", Content: content})

	// Stream response
	response, err := b.ai.Complete(ctx, messages)
	if err != nil {
		log.Printf("AI error: %v", err)
		return c.Send("❌ Sorry, I encountered an error processing your request.")
	}

	// Send response
	if err := c.Send(response); err != nil {
		return err
	}

	// Save session
	if err := b.store.SaveSession(c.Chat().ID, response); err != nil {
		log.Printf("Failed to save session: %v", err)
	}

	return nil
}
