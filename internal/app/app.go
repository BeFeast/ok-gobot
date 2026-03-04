package app

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/api"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/bot"
	"ok-gobot/internal/config"
	"ok-gobot/internal/control"
	"ok-gobot/internal/cron"
	"ok-gobot/internal/logger"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/memorymcp"
	"ok-gobot/internal/storage"
)

// App orchestrates all components
type App struct {
	mu            sync.RWMutex
	config        *config.Config
	store         *storage.Store
	bot           *bot.Bot
	ai            ai.Client
	personality   *agent.Personality
	memory        *agent.Memory
	scheduler     *cron.Scheduler
	memoryManager *memory.MemoryManager
	memoryMCP     *memorymcp.Server
	apiServer     *api.APIServer
	watcher       *config.ConfigWatcher
	controlServer *control.Server
	bootstraps    []*bootstrap.Watcher
	bootstrapSeen map[string]struct{}
}

// stateAdapter bridges bot/storage to the control.StateProvider interface.
type stateAdapter struct {
	b            *bot.Bot
	store        *storage.Store
	defaultModel string
}

func (a *stateAdapter) GetStatus() map[string]interface{} {
	return a.b.GetStatus()
}

func (a *stateAdapter) ListSessions() ([]control.SessionInfo, error) {
	rows, err := a.store.ListSessions(100)
	if err != nil {
		return nil, err
	}
	out := make([]control.SessionInfo, 0, len(rows))
	for _, r := range rows {
		chatID, _ := r["chat_id"].(int64)
		model, err := a.store.GetModelOverride(chatID)
		if err != nil || model == "" {
			model = a.defaultModel
		}
		out = append(out, control.SessionInfo{
			ChatID: chatID,
			Model:  model,
			State:  "idle",
		})
	}
	return out, nil
}

func (a *stateAdapter) SendChat(chatID int64, text string) error {
	return a.b.SendMessage(chatID, text)
}

func (a *stateAdapter) AbortRun(chatID int64) error {
	return a.b.AbortRun(chatID)
}

func (a *stateAdapter) RespondToApproval(id string, approved bool) error {
	return a.b.RespondToApproval(id, approved)
}

func (a *stateAdapter) SetModel(chatID int64, model string) error {
	return a.b.SetModel(chatID, model)
}

func (a *stateAdapter) SetAgent(chatID int64, agentName string) error {
	return a.b.SetAgent(chatID, agentName)
}

func (a *stateAdapter) SpawnSubagent(parentChatID int64, task, agentName string) error {
	msg := fmt.Sprintf("[subagent] task=%q agent=%q", task, agentName)
	return a.b.SendMessage(parentChatID, msg)
}

func (a *stateAdapter) SubmitTUIRun(ctx context.Context, req control.TUIRunRequest) <-chan agent.RunEvent {
	return a.b.SubmitTUIRun(ctx, req)
}

func (a *stateAdapter) AbortTUIRun(sessionKey string) {
	a.b.AbortTUIRun(sessionKey)
}

func (a *stateAdapter) LogTUIExchange(userText, assistantText string) {
	a.b.LogTUIExchange(userText, assistantText)
}

func (a *stateAdapter) GetStatusText(sessionID string) string {
	return a.b.GetStatusText(sessionID)
}

// New creates a new application instance
func New(cfg *config.Config, store *storage.Store) *App {
	return &App{
		config:        cfg,
		store:         store,
		bootstrapSeen: make(map[string]struct{}),
	}
}

// Start initializes and runs all components
func (a *App) Start(ctx context.Context) error {
	// Start config watcher if a config file path is known
	if a.config.ConfigPath != "" {
		watcher, err := config.NewConfigWatcher(a.config.ConfigPath, func(cfg *config.Config) {
			a.mu.Lock()
			a.config = cfg
			a.mu.Unlock()
			log.Printf("[config] Configuration reloaded from %s", cfg.ConfigPath)
		})
		if err != nil {
			log.Printf("[config] Failed to start config watcher: %v", err)
		} else {
			a.watcher = watcher
		}
	} else {
		log.Println("[config] No config file path set; config watcher disabled")
	}

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
	a.startBootstrapWatcher("default", personality)

	// Initialize agent registry
	var agentRegistry *agent.AgentRegistry
	if len(a.config.Agents) > 0 {
		log.Printf("🤖 Initializing agent registry with %d agents...", len(a.config.Agents))
		agentRegistry, err = agent.NewAgentRegistry(a.config.Agents, a.config.AI.Model, soulPath)
		if err != nil {
			return fmt.Errorf("failed to initialize agent registry: %w", err)
		}
		log.Printf("✅ Agent registry initialized with agents: %v", agentRegistry.List())
		for _, name := range agentRegistry.List() {
			profile := agentRegistry.Get(name)
			if profile == nil || profile.Personality == nil {
				continue
			}
			a.startBootstrapWatcher(name, profile.Personality)
		}
	} else {
		log.Println("🤖 No agents configured, using single default personality")
	}

	// Initialize memory system
	a.memory = agent.NewMemory(soulPath)

	aiAPIKey := strings.TrimSpace(a.config.AI.APIKey)
	if aiAPIKey == "" && a.config.AI.Provider == "anthropic" {
		if creds, err := ai.LoadAnthropicOAuthCredentials(""); err == nil && creds != nil {
			aiAPIKey = "oauth:" + creds.AccessToken
		}
	}

	// Initialize AI client if configured
	if aiAPIKey != "" || a.config.AI.Provider == "droid" {
		log.Printf("🤖 Initializing AI client (%s)...", a.config.AI.Provider)
		primaryCfg := ai.ProviderConfig{
			Name:    a.config.AI.Provider,
			APIKey:  aiAPIKey,
			Model:   a.config.AI.Model,
			BaseURL: a.config.AI.BaseURL,
		}
		droidCfg := ai.DroidConfig{
			BinaryPath: a.config.AI.Droid.BinaryPath,
			AutoLevel:  a.config.AI.Droid.AutoLevel,
			WorkDir:    a.config.AI.Droid.WorkDir,
		}
		if len(a.config.AI.FallbackModels) > 0 {
			log.Printf("🔄 Failover enabled: %d fallback model(s) configured", len(a.config.AI.FallbackModels))
			aiClient, err := ai.NewClientWithFailover(primaryCfg, a.config.AI.FallbackModels)
			if err != nil {
				return fmt.Errorf("failed to initialize AI client with failover: %w", err)
			}
			a.ai = aiClient
		} else {
			aiClient, err := ai.NewClientWithDroid(primaryCfg, droidCfg)
			if err != nil {
				return fmt.Errorf("failed to initialize AI client: %w", err)
			}
			a.ai = aiClient
		}
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

	// Initialize semantic memory manager if enabled
	if a.config.Memory.Enabled {
		apiKey := a.config.Memory.EmbeddingsAPIKey
		if apiKey == "" {
			apiKey = a.config.AI.APIKey
		}
		embClient := memory.NewEmbeddingClient(
			a.config.Memory.EmbeddingsBaseURL,
			apiKey,
			a.config.Memory.EmbeddingsModel,
		)
		memStore, err := memory.NewMemoryStore(a.store.DB())
		if err != nil {
			log.Printf("⚠️ Failed to initialize memory store: %v", err)
		} else {
			var options []memory.MemoryManagerOption

			if a.config.Memory.MetadataExtraction {
				metadataModel := strings.TrimSpace(a.config.Memory.MetadataModel)
				if metadataModel == "" {
					metadataModel = "haiku"
				}
				if fullModel, ok := a.config.ModelAliases[metadataModel]; ok {
					metadataModel = fullModel
				} else if fullModel, ok := config.DefaultModelAliases[metadataModel]; ok {
					metadataModel = fullModel
				}

				metadataClient, err := ai.NewClient(ai.ProviderConfig{
					Name:    a.config.AI.Provider,
					APIKey:  aiAPIKey,
					BaseURL: a.config.AI.BaseURL,
					Model:   metadataModel,
				})
				if err != nil {
					log.Printf("⚠️ Failed to initialize memory metadata extractor: %v", err)
				} else {
					options = append(options, memory.WithMetadataExtractor(memory.NewLLMMetadataExtractor(metadataClient)))
					log.Printf("🧠 Memory metadata extraction enabled (model: %s)", metadataModel)
				}
			}

			a.memoryManager = memory.NewMemoryManager(embClient, memStore, options...)
			log.Println("🧠 Semantic memory initialized")
		}
	}

	// Initialize and start memory MCP server if enabled
	if a.config.Memory.MCP.Enabled {
		mcpCfg := memorymcp.Config{
			Enabled:     a.config.Memory.MCP.Enabled,
			Host:        a.config.Memory.MCP.Host,
			Port:        a.config.Memory.MCP.Port,
			Endpoint:    a.config.Memory.MCP.Endpoint,
			AllowWrites: a.config.Memory.MCP.AllowWrites,
		}
		a.memoryMCP = memorymcp.New(mcpCfg, a.memoryManager, a.memory)
		go func() {
			if err := a.memoryMCP.Start(ctx); err != nil {
				log.Printf("[memory-mcp] server error: %v", err)
			}
		}()
		log.Printf("🧠 Memory MCP server enabled on %s (writes=%v)", a.memoryMCP.URL(), mcpCfg.AllowWrites)
	}

	// Initialize bot
	aiCfg := bot.AIConfig{
		Provider:        a.config.AI.Provider,
		Model:           a.config.AI.Model,
		APIKey:          aiAPIKey,
		BaseURL:         a.config.AI.BaseURL,
		FallbackModels:  a.config.AI.FallbackModels,
		ModelAliases:    a.config.ModelAliases,
		DefaultThinking: a.config.AI.DefaultThinking,
	}
	b, err := bot.New(a.config.Telegram.Token, a.store, a.ai, aiCfg, a.personality, agentRegistry, a.config.Auth, a.config.Groups, a.config.TTS, a.scheduler, a.memoryManager)
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

	// Initialize and start control server if enabled
	if a.config.Control.Enabled {
		ctrlCfg := control.Config{
			Enabled:                   a.config.Control.Enabled,
			Port:                      a.config.Control.Port,
			Token:                     a.config.Control.Token,
			AllowLoopbackWithoutToken: a.config.Control.AllowLoopbackWithoutToken,
		}
		adapter := &stateAdapter{
			b:            a.bot,
			store:        a.store,
			defaultModel: a.config.AI.Model,
		}
		a.controlServer = control.New(ctrlCfg, adapter)
		a.bot.SetControlHub(a.controlServer.Hub())
		go func() {
			if err := a.controlServer.Start(ctx); err != nil {
				log.Printf("[control] server error: %v", err)
			}
		}()
		log.Printf("🔌 Control server listening on ws://127.0.0.1:%d/ws", a.config.Control.Port)
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
	if a.watcher != nil {
		a.watcher.Stop()
	}
	for _, watcher := range a.bootstraps {
		watcher.Stop()
	}
	if a.scheduler != nil {
		a.scheduler.Stop()
	}
	if a.apiServer != nil {
		ctx := context.Background()
		if err := a.apiServer.Stop(ctx); err != nil {
			log.Printf("Error stopping API server: %v", err)
		}
	}
	if a.memoryMCP != nil {
		ctx := context.Background()
		if err := a.memoryMCP.Stop(ctx); err != nil {
			log.Printf("Error stopping memory MCP server: %v", err)
		}
	}
	return nil
}

func (a *App) startBootstrapWatcher(name string, personality *agent.Personality) {
	if personality == nil || personality.BasePath == "" {
		return
	}

	if _, exists := a.bootstrapSeen[personality.BasePath]; exists {
		return
	}

	watcher, err := bootstrap.NewWatcher(personality.BasePath, func() {
		if err := personality.Reload(); err != nil {
			log.Printf("[bootstrap] failed to reload %s bootstrap from %s: %v", name, personality.BasePath, err)
			return
		}
		log.Printf("system prompt reloaded (%s from %s)", name, personality.BasePath)
	})
	if err != nil {
		log.Printf("[bootstrap] failed to start watcher for %s bootstrap at %s: %v", name, personality.BasePath, err)
		return
	}

	a.bootstraps = append(a.bootstraps, watcher)
	a.bootstrapSeen[personality.BasePath] = struct{}{}
}
