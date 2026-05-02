package app

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/api"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/bot"
	"ok-gobot/internal/config"
	"ok-gobot/internal/control"
	"ok-gobot/internal/cron"
	"ok-gobot/internal/delegation"
	"ok-gobot/internal/evolution"
	"ok-gobot/internal/hygiene"
	"ok-gobot/internal/logger"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/memorymcp"
	"ok-gobot/internal/role"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/supervisor"
	"ok-gobot/internal/tools"
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
	memoryStatus  *memory.StatusReporter
	memoryMCP     *memorymcp.Server
	memoryWatcher *memory.Watcher
	apiServer     *api.APIServer
	watcher       *config.ConfigWatcher
	controlServer *control.Server
	bootstraps    []*bootstrap.Watcher
	bootstrapSeen map[string]struct{}
}

// stateAdapter bridges bot/storage to the control.StateProvider interface.
type stateAdapter struct {
	b   *bot.Bot
	cfg *config.Config
}

func (a *stateAdapter) GetStatus() map[string]interface{} {
	return a.b.GetStatus()
}

func (a *stateAdapter) GetMemoryStatus(ctx context.Context) (memory.IndexStatus, error) {
	return a.b.GetMemoryStatus(ctx)
}

func (a *stateAdapter) GetSupervisorStatus() supervisor.Status {
	return a.b.GetSupervisorStatus()
}

func (a *stateAdapter) GetStore() *storage.Store {
	return a.b.GetStore()
}

func (a *stateAdapter) GetAgentRegistry() *agent.AgentRegistry {
	return a.b.GetAgentRegistry()
}

func (a *stateAdapter) GetScheduler() tools.CronScheduler {
	return a.b.GetScheduler()
}

func (a *stateAdapter) GetHygieneReport(ctx context.Context) (hygiene.Report, error) {
	var approvals []hygiene.Approval
	for _, pending := range a.b.PendingApprovals() {
		approvals = append(approvals, hygiene.Approval{
			ID:         pending.ID,
			SessionKey: fmt.Sprintf("telegram:%d", pending.ChatID),
			Command:    pending.Command,
			CreatedAt:  pending.CreatedAt,
		})
	}
	workers := hygieneWorkers(a.b.SubagentHub(), time.Now())
	return hygiene.BuildReport(ctx, hygiene.CollectOptions{
		Config:    a.cfg,
		Store:     a.b.GetStore(),
		Approvals: approvals,
		Workers:   workers,
	}, hygiene.Options{})
}

func hygieneWorkers(hub *runtime.Hub, seenAt time.Time) []hygiene.Worker {
	if hub == nil {
		return nil
	}
	snapshots := hub.ListWorkers()
	workers := make([]hygiene.Worker, 0, len(snapshots))
	for _, snapshot := range snapshots {
		workers = append(workers, hygiene.Worker{
			SessionKey: snapshot.SessionKey,
			Running:    snapshot.Running,
			Alive:      true,
			LastSeenAt: seenAt,
			QueueDepth: snapshot.QueueDepth,
		})
	}
	return workers
}

func (a *stateAdapter) RespondToApproval(id string, approved bool) error {
	return a.b.RespondToApproval(id, approved)
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

// dataProvider implements api.DataProvider by bridging storage and the runtime hub.
type dataProvider struct {
	store *storage.Store
	bot   *bot.Bot
}

func (d *dataProvider) ListJobs(status string, limit int) ([]storage.Job, error) {
	return d.store.ListJobsByStatus(status, limit)
}

func (d *dataProvider) GetJob(jobID string) (*storage.Job, error) {
	return d.store.GetJob(jobID)
}

func (d *dataProvider) GetJobEvents(jobID string, limit int) ([]storage.JobEvent, error) {
	return d.store.ListJobEvents(jobID, limit)
}

func (d *dataProvider) GetJobArtifacts(jobID string, limit int) ([]storage.JobArtifact, error) {
	return d.store.ListJobArtifacts(jobID, limit)
}

func (d *dataProvider) GetJobArtifact(artifactID int64) (*storage.JobArtifact, error) {
	return d.store.GetJobArtifact(artifactID)
}

func (d *dataProvider) CancelJob(jobID string) error {
	return d.store.UpdateJobCancelRequested(jobID, true)
}

func (d *dataProvider) WorkerSnapshots() []runtime.WorkerSnapshot {
	hub := d.bot.SubagentHub()
	if hub == nil {
		return nil
	}
	return hub.ListWorkers()
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
	loaderOptions := bootstrap.LoaderOptions{TrustWorkspaceScripts: a.config.TrustWorkspaceScripts()}
	log.Printf("🧠 Loading personality from %s...", soulPath)
	personality, err := agent.NewPersonalityWithOptions(soulPath, loaderOptions)
	if err != nil {
		log.Printf("⚠️ Failed to load personality: %v", err)
		// Continue - NewPersonality already handles missing files gracefully
		personality = &agent.Personality{}
	}
	a.personality = personality
	personality.SetScoreProvider(a.store)
	log.Printf("🦞 Personality loaded: %s %s", personality.GetName(), personality.GetEmoji())
	a.startBootstrapWatcher("default", personality)

	// Initialize agent registry
	var agentRegistry *agent.AgentRegistry
	if len(a.config.Agents) > 0 {
		log.Printf("🤖 Initializing agent registry with %d agents...", len(a.config.Agents))
		agentRegistry, err = agent.NewAgentRegistryWithOptions(a.config.Agents, a.config.AI.Model, soulPath, loaderOptions)
		if err != nil {
			return fmt.Errorf("failed to initialize agent registry: %w", err)
		}
		log.Printf("✅ Agent registry initialized with agents: %v", agentRegistry.List())
		for _, name := range agentRegistry.List() {
			profile := agentRegistry.Get(name)
			if profile == nil || profile.Personality == nil {
				continue
			}
			profile.Personality.SetScoreProvider(a.store)
			a.startBootstrapWatcher(name, profile.Personality)
		}
	} else {
		log.Println("🤖 No agents configured, using single default personality")
	}

	// Initialize memory system
	a.memory = agent.NewMemory(soulPath)
	a.memoryStatus = memory.NewStatusReporter(nil, memory.StatusOptions{
		Enabled:      a.config.Memory.Enabled,
		RootPath:     soulPath,
		BackendType:  appMemoryBackendName(a.config),
		WatcherState: memory.WatcherStateDisabled,
		ExtraPaths:   appMemoryExtraPathLabels(a.config),
		QMDStatus:    appMemoryQMDStatus(ctx, a.config),
	})

	aiAPIKey := strings.TrimSpace(a.config.AI.APIKey)
	if aiAPIKey == "" && a.config.AI.Provider == "anthropic" {
		if creds, err := ai.LoadAnthropicOAuthCredentials(""); err == nil && creds != nil {
			aiAPIKey = "oauth:" + creds.AccessToken
		}
	}

	activeAIModel := a.config.AI.Model
	var backendHealth ai.BackendHealth
	var backendPreflight *ai.BackendPreflight

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

		backendPreflight = ai.NewBackendPreflight(ai.BackendPreflightConfig{
			Provider:        primaryCfg,
			Droid:           droidCfg,
			FallbackModels:  a.config.AI.FallbackModels,
			FallbackEnabled: len(a.config.AI.FallbackModels) > 0,
		})
		preflightCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		backendHealth, err = backendPreflight.Check(preflightCtx, primaryCfg.Model, "default", a.config.AI.DefaultThinking)
		cancel()
		if err != nil {
			return fmt.Errorf("backend preflight failed: %w", err)
		}
		log.Printf("✅ Backend preflight passed (%s health=%s fallback=%s)", backendHealth.Identity.String(), backendHealth.Status, backendHealth.Fallback.Action)
		if backendHealth.Identity.Model != "" {
			activeAIModel = backendHealth.Identity.Model
			primaryCfg.Model = activeAIModel
		}

		fallbackModels := remainingFallbackModels(activeAIModel, a.config.AI.Model, a.config.AI.FallbackModels)
		if len(fallbackModels) > 0 {
			log.Printf("🔄 Failover enabled: %d fallback model(s) configured", len(fallbackModels))
			aiClient, err := ai.NewClientWithFailover(primaryCfg, fallbackModels)
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
		log.Printf("✅ AI client ready (model: %s)", activeAIModel)
	}

	// Initialize durable job service for background work
	jobService := runtime.NewJobService(a.store)
	jobService.SetArtifactRoots(a.config.Artifacts.Roots)

	// Initialize cron scheduler
	a.scheduler = cron.NewScheduler(a.store, func(ctx context.Context, job storage.CronJob, budget *delegation.Job) error {
		log.Printf("📅 Executing cron job #%d: %s", job.ID, job.Task)
		if a.bot == nil {
			return fmt.Errorf("bot not initialized")
		}
		return a.bot.RunCronTask(ctx, job.ChatID, job.Task, budget)
	})
	a.scheduler.SetNotifier(func(chatID int64, message string) {
		if a.bot != nil {
			a.bot.SendMessage(chatID, message) //nolint:errcheck
		}
	})
	a.scheduler.SetJobService(jobService)
	a.scheduler.SetReportDeliverer(func(chatID int64, report cron.JobReport) {
		if a.bot != nil {
			a.bot.SendMessage(chatID, report.FormatTelegram()) //nolint:errcheck
		}
	})

	// Start cron scheduler
	if err := a.scheduler.Start(ctx); err != nil {
		log.Printf("⚠️ Failed to start cron scheduler: %v", err)
	} else {
		log.Println("📅 Cron scheduler started")
	}

	// Auto-register role cron jobs from roles_path (if configured).
	if a.config.RolesPath != "" {
		manifests, loadErrs := role.LoadDirLenient(a.config.RolesPath)
		for _, e := range loadErrs {
			log.Printf("⚠️ [roles] skipping invalid manifest: %v", e)
		}
		if len(manifests) > 0 {
			if err := a.scheduler.RegisterRoleJobs(manifests, a.config.Auth.AdminID); err != nil {
				log.Printf("⚠️ [roles] failed to register role jobs: %v", err)
			} else {
				log.Printf("🎭 Loaded %d role(s) from %s", len(manifests), a.config.RolesPath)
			}
		}
	}

	// Initialize semantic memory manager if enabled
	if a.config.Memory.Enabled {
		a.memoryStatus.SetWatcherState(memory.WatcherStateStarting)
		apiKey := a.config.Memory.EmbeddingsAPIKey
		if apiKey == "" {
			apiKey = a.config.AI.APIKey
		}
		var embClient *memory.EmbeddingClient
		if memory.EmbeddingProviderConfigured(a.config.Memory.EmbeddingsBaseURL, apiKey) {
			embClient = memory.NewEmbeddingClient(
				a.config.Memory.EmbeddingsBaseURL,
				apiKey,
				a.config.Memory.EmbeddingsModel,
			)
		}
		memStore, err := memory.NewMemoryStore(a.store.DB())
		if err != nil {
			log.Printf("⚠️ Failed to initialize memory store: %v", err)
			a.memoryStatus.SetWatcherState(memory.WatcherStateError)
			a.memoryStatus.SetLastError("memory store initialization failed", err)
		} else {
			a.memoryStatus.SetStore(memStore)
			var options []memory.MemoryManagerOption
			builtinBackend := memory.NewBuiltinBackend(embClient, memStore)

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

			backendName := strings.ToLower(strings.TrimSpace(a.config.Memory.Backend))
			if backendName == "qmd" || backendName == "auto" {
				qmdBackend := memory.NewQMDBackend(appQMDConfig(a.config.Memory.QMD))
				cooldown := parseDurationOrDefault(a.config.Memory.QMD.FallbackCooldown, time.Minute)
				fallbackBackend := memory.NewFallbackBackend(qmdBackend, builtinBackend, cooldown)
				options = append(options, memory.WithBackend(fallbackBackend))
				a.memoryStatus.SetQMDStatusFunc(func(ctx context.Context) string {
					return appMemoryQMDRuntimeStatus(ctx, a.config, qmdBackend, fallbackBackend)
				})
				log.Printf("🧠 QMD memory backend configured (mode=%s, fallback=builtin)", a.config.Memory.QMD.SearchMode)
			}

			a.memoryManager = memory.NewMemoryManager(embClient, memStore, options...)
			if embClient == nil {
				log.Println("🧠 Memory initialized (lexical search only; embeddings not configured)")
			} else {
				log.Println("🧠 Hybrid memory initialized")
			}
			a.startMemoryIndexer(ctx, soulPath, memStore, embClient, a.memoryStatus)
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
		Model:           activeAIModel,
		ModelTier:       "default",
		APIKey:          aiAPIKey,
		BaseURL:         a.config.AI.BaseURL,
		FallbackModels:  a.config.AI.FallbackModels,
		ModelAliases:    a.config.ModelAliases,
		DefaultThinking: a.config.AI.DefaultThinking,
		BackendHealth:   backendHealth,
		Routing:         a.config.AI.Routing,
		MemoryMode:      config.NormalizeMemoryMode(a.config.Memory.Mode),
	}
	if backendPreflight != nil {
		aiCfg.BackendPreflight = backendPreflight.Check
	}
	memoryExtras, err := a.normalizedExtraPaths()
	if err != nil {
		log.Printf("⚠️ [memory] extra paths config error: %v", err)
		memoryExtras = nil
	}
	b, err := bot.New(a.config.Telegram.Token, a.store, a.ai, aiCfg, a.personality, agentRegistry, a.config.Auth, a.config.Groups, a.config.TTS, a.config.Browser, a.config.STT, a.scheduler, a.memoryManager, memoryExtras, a.config.Memory.Sessions.Enabled, a.memoryStatus, a.config.Contacts)
	if err != nil {
		return fmt.Errorf("failed to create bot: %w", err)
	}
	a.bot = b
	a.bot.SetArtifactRoots(a.config.Artifacts.Roots)

	// Wire Active Memory pre-reply recall (DM-only, opt-in).
	activeCfg := agent.ActiveMemoryConfig{
		Enabled:      a.config.Memory.Active.Enabled,
		Timeout:      time.Duration(a.config.Memory.Active.TimeoutMS) * time.Millisecond,
		MaxSnippets:  a.config.Memory.Active.MaxSnippets,
		MaxChars:     a.config.Memory.Active.MaxChars,
		HistoryTurns: a.config.Memory.Active.HistoryTurns,
	}
	b.ConfigureActiveMemory(a.memoryManager, activeCfg)
	if activeCfg.Enabled {
		if a.memoryManager == nil {
			log.Printf("⚠️  Active Memory enabled in config but memory backend is unavailable — pre-reply recall will report no_backend")
		} else {
			log.Printf("🧠 Active Memory pre-reply recall enabled (timeout=%dms max_snippets=%d max_chars=%d)",
				a.config.Memory.Active.TimeoutMS, a.config.Memory.Active.MaxSnippets, a.config.Memory.Active.MaxChars)
		}
	}

	// Initialize self-evolution engine if enabled.
	if a.config.Evolution.Enabled {
		evoCfg := evolution.Config{
			Enabled:        a.config.Evolution.Enabled,
			TasksPerCycle:  a.config.Evolution.TasksPerCycle,
			PassThreshold:  a.config.Evolution.PassThreshold,
			MaxDiffPercent: a.config.Evolution.MaxDiffPercent,
			BenchmarksDir:  a.config.Evolution.BenchmarksDir,
			EvolutionDir:   a.config.Evolution.EvolutionDir,
		}
		evoEngine := evolution.New(evoCfg, a.store)
		b.SetTaskObserver(evoEngine)
		log.Printf("🧬 Self-evolution loop enabled (tasks_per_cycle=%d threshold=%.0f%%)",
			evoCfg.TasksPerCycle, evoCfg.PassThreshold*100)
	}

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
		a.apiServer.SetDataProvider(&dataProvider{store: a.store, bot: a.bot})
		a.apiServer.SetHygieneProvider(&stateAdapter{b: a.bot, cfg: a.config})
		a.apiServer.SetArtifactRoots(a.config.Artifacts.Roots)

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
		adapter := &stateAdapter{b: a.bot, cfg: a.config}
		a.controlServer = control.New(ctrlCfg, adapter)
		a.controlServer.SetStore(a.store)
		a.controlServer.SetArtifactRoots(a.config.Artifacts.Roots)
		a.bot.SetControlHub(a.controlServer.Hub())
		go func() {
			if err := a.controlServer.Start(ctx); err != nil {
				log.Printf("[control] server error: %v", err)
			}
		}()
		log.Printf("🔌 Control server listening on ws://127.0.0.1:%d/ws", a.config.Control.Port)
	}

	// Wire up roles path so Telegram commands can load role manifests.
	if a.config.RolesPath != "" {
		a.bot.SetRolesPath(a.config.RolesPath)
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
	if a.memoryWatcher != nil {
		a.memoryWatcher.Stop()
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

func appQMDConfig(cfg config.MemoryQMDConfig) memory.QMDConfig {
	return memory.QMDConfig{
		BinaryPath: cfg.BinaryPath,
		Index:      cfg.Index,
		IndexPath:  cfg.IndexPath,
		SearchMode: cfg.SearchMode,
		Timeout:    parseDurationOrDefault(cfg.Timeout, memory.DefaultQMDTimeout),
		Collections: memory.QMDCollections{
			Workspace:          cfg.Collections.Workspace,
			DailyNotes:         cfg.Collections.DailyNotes,
			SessionTranscripts: cfg.Collections.SessionTranscripts,
			ExtraPaths:         cfg.Collections.ExtraPaths,
		},
	}
}

func appMemoryBackendName(cfg *config.Config) string {
	if cfg == nil {
		return "builtin"
	}
	name := strings.ToLower(strings.TrimSpace(cfg.Memory.Backend))
	if name == "" {
		return "builtin"
	}
	return name
}

func appMemoryQMDStatus(ctx context.Context, cfg *config.Config) string {
	return appMemoryQMDRuntimeStatus(ctx, cfg, nil, nil)
}

func appMemoryQMDRuntimeStatus(ctx context.Context, cfg *config.Config, qmdBackend *memory.QMDBackend, fallbackBackend *memory.FallbackBackend) string {
	backend := appMemoryBackendName(cfg)
	if cfg == nil || !cfg.Memory.Enabled {
		return "skipped (memory.enabled=false)"
	}
	if backend == "qmd" || backend == "auto" {
		if qmdBackend == nil {
			qmdBackend = memory.NewQMDBackend(appQMDConfig(cfg.Memory.QMD))
		}
		runtimeReason := ""
		if fallbackBackend != nil {
			runtimeReason = fallbackBackend.FallbackReason()
		}
		return qmdBackend.Diagnostics(ctx).RuntimeStatus(runtimeReason)
	}
	return fmt.Sprintf("skipped (memory.backend=%s)", backend)
}

func appMemoryExtraPathLabels(cfg *config.Config) []string {
	if cfg == nil || len(cfg.Memory.ExtraPaths) == 0 {
		return nil
	}
	labels := make([]string, 0, len(cfg.Memory.ExtraPaths))
	for _, extra := range cfg.Memory.ExtraPaths {
		name := strings.TrimSpace(extra.Name)
		path := strings.TrimSpace(extra.Path)
		switch {
		case name != "" && path != "":
			labels = append(labels, fmt.Sprintf("%s=%s", name, path))
		case name != "":
			labels = append(labels, name)
		case path != "":
			labels = append(labels, path)
		}
	}
	return labels
}

func parseDurationOrDefault(value string, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fallback
	}
	return duration
}

func remainingFallbackModels(activeModel, configuredPrimary string, configuredFallbacks []string) []string {
	activeModel = strings.TrimSpace(activeModel)
	configuredPrimary = strings.TrimSpace(configuredPrimary)
	order := make([]string, 0, 1+len(configuredFallbacks))
	if configuredPrimary != "" {
		order = append(order, configuredPrimary)
	}
	for _, model := range configuredFallbacks {
		model = strings.TrimSpace(model)
		if model != "" {
			order = append(order, model)
		}
	}

	remaining := []string{}
	seenActive := activeModel == ""
	seen := map[string]struct{}{}
	for _, model := range order {
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		if !seenActive {
			seenActive = model == activeModel
			continue
		}
		if model != activeModel {
			remaining = append(remaining, model)
		}
	}
	return remaining
}

func (a *App) startMemoryIndexer(ctx context.Context, rootPath string, store *memory.MemoryStore, embedder memory.EmbeddingBatchClient, reporter *memory.StatusReporter) {
	if strings.TrimSpace(rootPath) == "" || store == nil {
		return
	}

	indexer := memory.NewIndexer(rootPath, store, embedder)
	stats, err := memory.IndexManagedSources(ctx, rootPath, indexer)
	if err != nil {
		log.Printf("⚠️ [memory] initial index failed: %v", err)
		reporter.SetLastError("initial index failed", err)
	} else {
		reporter.ClearLastError()
		log.Printf("🧠 [memory] indexed %d managed source file(s)", stats.FilesIndexed)
	}

	extras, extraErr := a.normalizedExtraPaths()
	if extraErr != nil {
		log.Printf("⚠️ [memory] extra paths config error: %v", extraErr)
		reporter.SetLastError("extra paths config error", extraErr)
	}
	if len(extras) > 0 {
		extraStats, errs := memory.IndexExtraPaths(ctx, extras, indexer)
		log.Printf("🧠 [memory] indexed %d extra source file(s) across %d collection(s)", extraStats.FilesIndexed, len(extras))
		for _, e := range errs {
			log.Printf("⚠️ [memory] extra path indexing: %v", e)
			reporter.SetLastError("extra path indexing failed", e)
		}
	}

	watcher, err := memory.NewWatcher(rootPath)
	if err != nil {
		log.Printf("⚠️ [memory] failed to start memory watcher: %v", err)
		reporter.SetWatcherState(memory.WatcherStateError)
		reporter.SetLastError("memory watcher failed", err)
	} else {
		a.memoryWatcher = watcher
		reporter.SetWatcherState(memory.WatcherStateActive)
		go a.runMemoryWatcher(ctx, rootPath, watcher, indexer, reporter)
		log.Printf("🧠 [memory] watcher active for %s", rootPath)
	}

	for _, extra := range extras {
		extraWatcher, err := memory.NewWatcher(extra.Path)
		if err != nil {
			log.Printf("⚠️ [memory] extra path %q watcher unavailable (%v) — index won't auto-refresh", extra.Name, err)
			reporter.SetLastError(fmt.Sprintf("extra path %q watcher unavailable", extra.Name), err)
			continue
		}
		if reporter.WatcherState() != memory.WatcherStateError {
			reporter.SetWatcherState(memory.WatcherStateActive)
		}
		go a.runExtraPathWatcher(ctx, extra, extraWatcher, indexer, reporter)
		log.Printf("🧠 [memory] watcher active for extra path %q (%s)", extra.Name, extra.Path)
	}
}

func (a *App) runMemoryWatcher(ctx context.Context, rootPath string, watcher *memory.Watcher, indexer *memory.Indexer, reporter *memory.StatusReporter) {
	for {
		select {
		case <-ctx.Done():
			watcher.Stop()
			return
		case err, ok := <-watcher.Errors():
			if !ok {
				return
			}
			log.Printf("⚠️ [memory] watcher error: %v", err)
			reporter.SetWatcherState(memory.WatcherStateError)
			reporter.SetLastError("memory watcher error", err)
		case event, ok := <-watcher.Events():
			if !ok {
				return
			}
			rel, ok := memory.ManagedRelativePath(rootPath, event.Path)
			if !ok {
				continue
			}
			if err := indexer.IndexFile(ctx, event.Path, rel); err != nil {
				log.Printf("⚠️ [memory] reindex failed for %s: %v", rel, err)
				reporter.SetLastError("reindex failed for "+rel, err)
				continue
			}
			reporter.SetWatcherState(memory.WatcherStateActive)
			reporter.ClearLastError()
			log.Printf("🧠 [memory] reindexed %s", rel)
		}
	}
}

func (a *App) runExtraPathWatcher(ctx context.Context, extra memory.ExtraPath, watcher *memory.Watcher, indexer *memory.Indexer, reporter *memory.StatusReporter) {
	for {
		select {
		case <-ctx.Done():
			watcher.Stop()
			return
		case err, ok := <-watcher.Errors():
			if !ok {
				return
			}
			log.Printf("⚠️ [memory] extra path %q watcher error: %v", extra.Name, err)
			reporter.SetWatcherState(memory.WatcherStateError)
			reporter.SetLastError(extraWatcherError(extra.Name), err)
		case event, ok := <-watcher.Events():
			if !ok {
				return
			}
			rel, ok := memory.ExtraPathRelative(extra, event.Path)
			if !ok {
				continue
			}
			label := memory.SourceLabelForExtra(extra.Name, rel)
			reindexError := "reindex failed for " + label
			if err := indexer.IndexFile(ctx, event.Path, label); err != nil {
				log.Printf("⚠️ [memory] reindex failed for %s: %v", label, err)
				reporter.SetLastError(reindexError, err)
				continue
			}
			if reporter.ClearLastErrorIfPrefix(extraWatcherError(extra.Name), reindexError) {
				reporter.SetWatcherState(memory.WatcherStateActive)
			} else if reporter.WatcherState() != memory.WatcherStateError {
				reporter.SetWatcherState(memory.WatcherStateActive)
				reporter.ClearLastError()
			}
			log.Printf("🧠 [memory] reindexed %s", label)
		}
	}
}

func extraWatcherError(name string) string {
	return fmt.Sprintf("extra path %q watcher error", name)
}

func (a *App) normalizedExtraPaths() ([]memory.ExtraPath, error) {
	if a.config == nil || len(a.config.Memory.ExtraPaths) == 0 {
		return nil, nil
	}
	raw := make([]memory.RawExtraPath, 0, len(a.config.Memory.ExtraPaths))
	for _, e := range a.config.Memory.ExtraPaths {
		raw = append(raw, memory.RawExtraPath{
			Name:     e.Name,
			Path:     e.Path,
			Patterns: e.Patterns,
			ReadOnly: e.ReadOnly,
			Scope:    e.Scope,
		})
	}
	return memory.NormalizeExtraPaths(raw)
}
