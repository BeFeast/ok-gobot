package app

import (
	"context"
	"fmt"
	"log"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/bot"
	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

// App orchestrates all components
type App struct {
	config      *config.Config
	store       *storage.Store
	bot         *bot.Bot
	ai          ai.Client
	personality *agent.Personality
	memory      *agent.Memory
}

// New creates a new application instance
func New(cfg *config.Config, store *storage.Store) *App {
	return &App{
		config: cfg,
		store:  store,
	}
}

// Start initializes and runs all components
func (a *App) Start(ctx context.Context) error {
	// Load personality from configured directory
	soulPath := a.config.GetSoulPath()
	log.Printf("🧠 Loading personality from %s...", soulPath)
	personality, err := agent.NewPersonality(soulPath)
	if err != nil {
		log.Printf("⚠️ Failed to load personality: %v", err)
		// Continue - NewPersonality already handles missing files gracefully
		personality = &agent.Personality{}
	}
	a.personality = personality
	log.Printf("🦞 Personality loaded: %s %s", personality.GetName(), personality.GetEmoji())

	// Initialize memory system
	a.memory = agent.NewMemory("")

	// Initialize AI client if configured
	if a.config.AI.APIKey != "" {
		log.Printf("🤖 Initializing AI client (%s)...", a.config.AI.Provider)
		aiClient, err := ai.NewClient(ai.ProviderConfig{
			Name:    a.config.AI.Provider,
			APIKey:  a.config.AI.APIKey,
			Model:   a.config.AI.Model,
			BaseURL: a.config.AI.BaseURL,
		})
		if err != nil {
			return fmt.Errorf("failed to initialize AI client: %w", err)
		}
		a.ai = aiClient
		log.Printf("✅ AI client ready (model: %s)", a.config.AI.Model)
	}

	// Initialize bot
	aiCfg := bot.AIConfig{
		Provider: a.config.AI.Provider,
		Model:    a.config.AI.Model,
		APIKey:   a.config.AI.APIKey,
	}
	b, err := bot.New(a.config.Telegram.Token, a.store, a.ai, aiCfg, a.personality)
	if err != nil {
		return fmt.Errorf("failed to create bot: %w", err)
	}
	a.bot = b

	// Start bot
	return a.bot.Start(ctx)
}

// Stop gracefully shuts down all components
func (a *App) Stop() error {
	// Cleanup is handled via context cancellation
	return nil
}
