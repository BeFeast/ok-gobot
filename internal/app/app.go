package app

import (
	"context"
	"fmt"

	"moltbot/internal/ai"
	"moltbot/internal/bot"
	"moltbot/internal/config"
	"moltbot/internal/storage"
)

// App orchestrates all components
type App struct {
	config *config.Config
	store  *storage.Store
	bot    *bot.Bot
	ai     ai.Client
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
	// Initialize AI client if configured
	if a.config.AI.APIKey != "" {
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
	}

	// Initialize bot
	aiCfg := bot.AIConfig{
		Provider: a.config.AI.Provider,
		Model:    a.config.AI.Model,
		APIKey:   a.config.AI.APIKey,
	}
	b, err := bot.New(a.config.Telegram.Token, a.store, a.ai, aiCfg)
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
