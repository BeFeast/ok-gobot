package app

import (
	"context"
	"fmt"
	"log"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/api"
	"ok-gobot/internal/bot"
	"ok-gobot/internal/config"
	"ok-gobot/internal/cron"
	"ok-gobot/internal/logger"
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
	scheduler   *cron.Scheduler
	apiServer   *api.APIServer
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
	// Set log level from config
	logger.SetLevel(a.config.LogLevel)

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

	// Initialize agent registry
	var agentRegistry *agent.AgentRegistry
	if len(a.config.Agents) > 0 {
		log.Printf("🤖 Initializing agent registry with %d agents...", len(a.config.Agents))
		agentRegistry, err = agent.NewAgentRegistry(a.config.Agents, a.config.AI.Model, soulPath)
		if err != nil {
			return fmt.Errorf("failed to initialize agent registry: %w", err)
		}
		log.Printf("✅ Agent registry initialized with agents: %v", agentRegistry.List())
	} else {
		log.Println("🤖 No agents configured, using single default personality")
	}

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

	// Initialize cron scheduler
	a.scheduler = cron.NewScheduler(a.store, func(ctx context.Context, job storage.CronJob) error {
		log.Printf("📅 Executing cron job: %s", job.Task)
		// TODO: Process job.Task through agent
		return nil
	})

	// Start cron scheduler
	if err := a.scheduler.Start(ctx); err != nil {
		log.Printf("⚠️ Failed to start cron scheduler: %v", err)
	} else {
		log.Println("📅 Cron scheduler started")
	}

	// Initialize bot
	aiCfg := bot.AIConfig{
		Provider:     a.config.AI.Provider,
		Model:        a.config.AI.Model,
		APIKey:       a.config.AI.APIKey,
		ModelAliases: a.config.ModelAliases,
	}
	b, err := bot.New(a.config.Telegram.Token, a.store, a.ai, aiCfg, a.personality, agentRegistry, a.config.Auth, a.config.Groups, a.config.TTS)
	if err != nil {
		return fmt.Errorf("failed to create bot: %w", err)
	}
	a.bot = b

	// Initialize approval system
	log.Println("🔒 Setting up command approval system...")
	b.InitializeApprovalSystem()
	b.RegisterApprovalHandlers()

	// Initialize and start API server if enabled
	if a.config.API.Enabled {
		if a.config.API.APIKey == "" {
			return fmt.Errorf("API enabled but api_key not configured")
		}
		log.Printf("🌐 Initializing API server on port %d...", a.config.API.Port)
		a.apiServer = api.NewAPIServer(a.config.API, a.bot)

		// Start API server in goroutine
		go func() {
			if err := a.apiServer.Start(ctx); err != nil {
				log.Printf("API server error: %v", err)
			}
		}()
	}

	// Start bot (this blocks until context is cancelled)
	return a.bot.Start(ctx)
}

// GetScheduler returns the cron scheduler for tool registration
func (a *App) GetScheduler() *cron.Scheduler {
	return a.scheduler
}

// Stop gracefully shuts down all components
func (a *App) Stop() error {
	if a.scheduler != nil {
		a.scheduler.Stop()
	}
	if a.apiServer != nil {
		ctx := context.Background()
		if err := a.apiServer.Stop(ctx); err != nil {
			log.Printf("Error stopping API server: %v", err)
		}
	}
	return nil
}
