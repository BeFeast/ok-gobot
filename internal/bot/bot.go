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
	"ok-gobot/internal/config"
	"ok-gobot/internal/control"
	"ok-gobot/internal/logger"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

// Bot wraps the Telegram bot with business logic.
// Telegram currently still uses the legacy agent.RuntimeHub compatibility path.
// New architecture work should target the chat/jobs mailbox runtime in internal/runtime.
type Bot struct {
	ctx              context.Context // bot lifetime context, set during Start
	api              *telebot.Bot
	store            *storage.Store
	ai               ai.Client
	streamingAI      ai.StreamingClient
	aiConfig         AIConfig
	personality      *agent.Personality
	agentRegistry    *agent.AgentRegistry
	toolRegistry     *tools.Registry
	safety           *agent.Safety
	memory           *agent.Memory
	authManager      *AuthManager
	groupManager     *GroupManager
	approvalManager  *ApprovalManager
	hub              *agent.RuntimeHub // legacy hub/subagent compatibility runtime for Telegram/TUI bridging
	subagentHub      *runtime.Hub      // event bus for sub-agent completion routing
	subagentNotifier *SubagentNotifier // routes child completions to parent Telegram chats
	adminID          int64
	enableStream     bool
	debouncer        *Debouncer
	rateLimiter      *RateLimiter
	configWatcher    ConfigWatcher
	usageTracker     *UsageTracker
	fragmentBuffer   *FragmentBuffer
	mediaGroupBuf    *MediaGroupBuffer
	queueManager     *QueueManager
	scheduler        tools.CronScheduler
	ackManager       *AckHandleManager
	controlHub       *control.Hub // optional: emit run/tool/approval events over WebSocket
}

// AIConfig holds AI configuration for status display
type AIConfig struct {
	Provider        string
	Model           string
	APIKey          string
	BaseURL         string
	FallbackModels  []string
	ModelAliases    map[string]string
	DefaultThinking string // Default thinking level when no session override is set
}

// New creates a new bot instance
func New(token string, store *storage.Store, aiClient ai.Client, aiCfg AIConfig, personality *agent.Personality, agentRegistry *agent.AgentRegistry, authCfg config.AuthConfig, groupsCfg config.GroupsConfig, ttsCfg config.TTSConfig, browserCfg config.BrowserConfig, scheduler tools.CronScheduler, memoryManager *memory.MemoryManager, contacts map[string]int64) (*Bot, error) {
	pref := telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}

	api, err := telebot.NewBot(pref)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	// Create tool registry with optional dependencies.
	// Cron tool is NOT registered here — it is created per-chat in the agent handler
	// so each job gets the correct chatID. Use personality.BasePath as the workspace
	// root so that file/path tools resolve relative paths against the configured soul
	// directory instead of the process working directory.
	toolsConfig := &tools.ToolsConfig{
		OpenAIAPIKey:    aiCfg.APIKey,
		TTSProvider:     ttsCfg.Provider,
		TTSVoice:        ttsCfg.DefaultVoice,
		ChromePath:      browserCfg.ChromePath,
		BrowserProfile:  browserCfg.ProfilePath,
		BrowserDebugURL: browserCfg.DebugURL,
		MemoryManager:   memoryManager,
		PatternStore:    store,
		EmergencyStop:   store,
	}
	toolRegistry, _ := tools.LoadFromConfigWithOptions(personality.BasePath, toolsConfig)

	// Try to cast aiClient to streaming client
	streamingClient, _ := aiClient.(ai.StreamingClient)

	// Create auth manager
	authManager := NewAuthManager(store, authCfg)

	// Create group manager
	groupManager := NewGroupManager(store, groupsCfg.DefaultMode, api.Me.Username)

	memoryBasePath := ""
	if personality != nil {
		memoryBasePath = personality.BasePath
	}

	b := &Bot{
		api:              api,
		store:            store,
		ai:               aiClient,
		streamingAI:      streamingClient,
		aiConfig:         aiCfg,
		personality:      personality,
		agentRegistry:    agentRegistry,
		toolRegistry:     toolRegistry,
		safety:           agent.NewSafety(),
		memory:           agent.NewMemory(memoryBasePath),
		authManager:      authManager,
		groupManager:     groupManager,
		approvalManager:  NewApprovalManager(api),
		subagentNotifier: NewSubagentNotifier(api),
		enableStream:     streamingClient != nil,
		debouncer:        NewDebouncer(1500 * time.Millisecond),
		rateLimiter:      NewRateLimiter(10, 1*time.Minute),
		usageTracker:     NewUsageTracker(),
		fragmentBuffer:   NewFragmentBuffer(),
		mediaGroupBuf:    NewMediaGroupBuffer(),
		queueManager:     NewQueueManager(),
		ackManager:       NewAckHandleManager(),
		scheduler:        scheduler,
	}

	// Register message tool: bot itself is the sender (self-reference is safe post-creation)
	msgTool := tools.NewMessageTool(b)
	for alias, chatID := range contacts {
		msgTool.AddAllowedChat(chatID, alias)
		log.Printf("📇 Message tool: added contact %s → %d", alias, chatID)
	}
	toolRegistry.Register(msgTool)

	// Register cron tool with chatID=0 as fallback. The RunResolver creates
	// per-chat cron tools so scheduled jobs carry the correct chatID.
	if scheduler != nil {
		toolRegistry.Register(tools.NewCronTool(scheduler, 0))
	}

	// Build the RunResolver — the RuntimeHub uses this to own agent creation,
	// tool registry filtering, and AI client lifecycle for every run.
	resolver := &agent.RunResolver{
		Store:              store,
		Registry:           agentRegistry,
		DefaultPersonality: personality,
		AIConfig: agent.AIResolverConfig{
			Provider:        aiCfg.Provider,
			Model:           aiCfg.Model,
			APIKey:          aiCfg.APIKey,
			BaseURL:         aiCfg.BaseURL,
			DefaultThinking: aiCfg.DefaultThinking,
			DefaultClient:   aiClient,
			ModelAliases:    aiCfg.ModelAliases,
		},
		ToolRegistry: toolRegistry,
		Scheduler:    scheduler,
	}
	b.hub = agent.NewRuntimeHub(resolver)

	// Wire hub as subagent submitter for browser_task tool.
	// Must be done after hub creation to break circular dependency.
	resolver.SubagentSubmitter = b.hub

	// Ensure today's memory file exists so file tool doesn't error on first read.
	if err := b.memory.EnsureTodayNote(); err != nil {
		log.Printf("[bot] warning: could not ensure today's memory note: %v", err)
	}

	return b, nil
}

// SendToChat implements tools.MessageSender, allowing the message tool to send
// Telegram messages through the live bot instance.
func (b *Bot) SendToChat(chatID int64, text string) error {
	return b.SendMessage(chatID, text)
}

// SendPhotoToChat implements tools.MediaSender, sending a photo file to a Telegram chat.
func (b *Bot) SendPhotoToChat(chatID int64, filePath, caption string) error {
	chat := &telebot.Chat{ID: chatID}
	photo := &telebot.Photo{
		File:    telebot.FromDisk(filePath),
		Caption: caption,
	}
	_, err := b.api.Send(chat, photo)
	if err != nil && strings.Contains(err.Error(), "PHOTO_INVALID_DIMENSIONS") {
		// Fallback: send as document when image dimensions exceed Telegram limits.
		doc := &telebot.Document{
			File:    telebot.FromDisk(filePath),
			Caption: caption,
		}
		_, err = b.api.Send(chat, doc)
	}
	return err
}

// EnableStreaming enables or disables streaming mode
func (b *Bot) EnableStreaming(enable bool) {
	b.enableStream = enable && b.streamingAI != nil
}

// registerCommands registers slash commands with Telegram BotFather API
func (b *Bot) registerCommands() {
	commands := []telebot.Command{
		{Text: "help", Description: "Show available commands"},
		{Text: "commands", Description: "List all slash commands"},
		{Text: "status", Description: "Show current status"},
		{Text: "whoami", Description: "Show your sender info"},
		{Text: "new", Description: "Start a new session"},
		{Text: "note", Description: "Quick note to today's memory"},
		{Text: "clear", Description: "Clear conversation history"},
		{Text: "stop", Description: "Stop the current run"},
		{Text: "abort", Description: "Abort the current run"},
		{Text: "memory", Description: "Show today's memory"},
		{Text: "tools", Description: "List available tools"},
		{Text: "model", Description: "Show or set AI model"},
		{Text: "agent", Description: "Manage agents (list/switch)"},
		{Text: "usage", Description: "Usage footer control"},
		{Text: "context", Description: "Explain how context is built"},
		{Text: "compact", Description: "Compact session context"},
		{Text: "think", Description: "Set thinking level"},
		{Text: "verbose", Description: "Toggle verbose mode"},
		{Text: "queue", Description: "Adjust queue settings"},
		{Text: "tts", Description: "Control text-to-speech"},
		{Text: "estop", Description: "Emergency stop dangerous tools"},
		{Text: "task", Description: "Spawn a sub-agent task"},
		{Text: "activate", Description: "Activate bot in group"},
		{Text: "standby", Description: "Set standby mode in group"},
		{Text: "pair", Description: "Pair with bot using code"},
		{Text: "auth", Description: "Authorization management"},
		{Text: "reload", Description: "Reload configuration"},
		{Text: "restart", Description: "Restart the bot"},
	}

	if err := b.api.SetCommands(commands); err != nil {
		log.Printf("Failed to register commands with BotFather: %v", err)
	} else {
		log.Printf("Registered %d commands with BotFather", len(commands))
	}
}

// Start begins processing updates
func (b *Bot) Start(ctx context.Context) error {
	b.ctx = ctx
	name := b.personality.GetName()
	emoji := b.personality.GetEmoji()

	// Register slash commands with Telegram
	b.registerCommands()

	// Register additional command handlers
	b.registerExtraHandlers()

	// Register media handlers (photo, voice, sticker, document)
	b.registerMediaHandlers(ctx)

	// Register migration handler (group -> supergroup)
	b.registerMigrationHandler(ctx)

	// Handle text messages
	b.api.Handle(telebot.OnText, func(c telebot.Context) error {
		return b.handleMessage(ctx, c)
	})

	// Handle commands
	b.api.Handle("/start", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		greeting := fmt.Sprintf("🦞 Welcome! I'm %s %s\n\n%s",
			name,
			emoji,
			"I'm your personal AI assistant. Just send me a message and I'll help you out.")
		return c.Send(greeting)
	}))

	b.api.Handle("/help", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		help := fmt.Sprintf(`🦞 %s Commands:

/start - Start the bot
/help - Show this help
/status - Check bot status
/clear - Clear conversation history
/note <text> - Quick note to today's memory
/memory - Show today's memory
/tools - List available tools
/model - Manage AI model (list/set/clear)
/agent - Manage agents (list/switch)
/auth - Authorization management (admin only)
/pair <code> - Pair with bot using pairing code
/estop on|off|status - Toggle dangerous tool families (admin only)
/reload - Reload configuration (admin only)`, name)
		return c.Send(help)
	}))

	b.api.Handle("/status", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleStatusCommand(c)
	}))

	b.api.Handle("/tools", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		var toolsList []string
		for _, t := range b.toolRegistry.List() {
			toolsList = append(toolsList, fmt.Sprintf("• %s: %s", t.Name(), t.Description()))
		}
		return c.Send(fmt.Sprintf("🔧 Available Tools:\n\n%s", strings.Join(toolsList, "\n")))
	}))

	b.api.Handle("/clear", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		if err := b.store.SaveSession(c.Chat().ID, ""); err != nil {
			return c.Send("❌ Failed to clear history")
		}
		return c.Send("✅ Conversation history cleared")
	}))

	b.api.Handle("/memory", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		note, err := b.memory.GetTodayNote()
		if err != nil {
			return c.Send("❌ Failed to load memory")
		}

		if note.Content == "" {
			return c.Send("📓 No entries for today yet")
		}

		return c.Send(fmt.Sprintf("📓 *Today's Memory*\n\n%s", note.Content),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}))

	b.api.Handle("/model", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleModelCommand(c)
	}))

	b.api.Handle("/activate", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleActivateCommand(c)
	}))

	b.api.Handle("/standby", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleStandbyCommand(c)
	}))

	b.api.Handle("/auth", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleAuthCommand(c)
	}))

	b.api.Handle("/pair", b.guardUnauthorizedDM(true, func(c telebot.Context) error {
		return b.handlePairCommand(c)
	}))

	b.api.Handle("/reload", b.guardUnauthorizedDM(false, func(c telebot.Context) error {
		return b.handleReloadCommand(c)
	}))

	// Start bot in goroutine
	go b.api.Start()

	// Start heartbeat in background
	go b.startHeartbeat()

	// Create the sub-agent event bus and start the completion notifier.
	b.subagentHub = runtime.NewHub(ctx, 64)
	go b.subagentNotifier.Run(ctx, b.subagentHub)

	log.Printf("🦞 %s started %s", name, emoji)

	// Wait for context cancellation
	<-ctx.Done()

	log.Println("Stopping bot...")
	b.debouncer.Stop()
	b.fragmentBuffer.Stop()
	b.mediaGroupBuf.Stop()
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

	logger.Debugf("Bot: message from user=%d (@%s) chat=%d len=%d", userID, username, chatID, len(content))

	// Check authorization before any state mutation (skip for /pair so users can
	// pair from an unauthorized state). DM messages route exclusively to
	// agent:main:main — the deny must fire here, before SaveMessage or
	// processViaHub, so no transcript/session state is touched on rejection.
	if !strings.HasPrefix(content, "/pair") && !b.authManager.CheckAccess(userID, chatID) {
		logDeniedAccess(userID, username, chatID, string(msg.Chat.Type))
		return c.Send("🔒 Not authorized. Please contact the bot administrator.")
	}

	// Check for stop phrase first — cancel any active run before confirming.
	if b.safety.IsStopPhrase(content) {
		sessionKey := sessionKeyForChat(msg.Chat)
		b.hub.Cancel(sessionKey)
		return c.Send(agent.GetStopPhraseResponse())
	}

	// Check if bot should respond in groups — do this BEFORE persisting to
	// memory/transcript so standby group traffic is never silently ingested.
	if !b.groupManager.ShouldRespond(chatID, msg, b.api.Me.Username) {
		logger.Debugf("Bot: skipping message in group chat=%d (standby)", chatID)
		return nil // Ignore message in standby mode without mention
	}

	// Log message (only after ShouldRespond so standby traffic is excluded)
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

	// Check rate limit first
	if !b.rateLimiter.Allow(chatID) {
		cooldown := b.rateLimiter.RemainingCooldown(chatID)
		seconds := int(cooldown.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		return c.Send(fmt.Sprintf("⏱️ Please wait %d seconds before sending another message.", seconds))
	}

	// Route through the runtime hub.
	if b.ai != nil {
		delivery := newTelegramDelivery(c)
		sessionKey := sessionKeyForChat(msg.Chat)
		preDecision := runtime.DecideChatRoute(content)
		if err := b.store.SaveSessionRoute(storage.SessionRoute{
			SessionKey:       string(sessionKey),
			Channel:          "telegram",
			ChatID:           chatID,
			ReplyToMessageID: msg.ID,
			UserID:           userID,
			Username:         username,
		}); err != nil {
			log.Printf("[bot] failed to persist session route for %s: %v", sessionKey, err)
		}

		if preDecision.Action == runtime.ChatActionReply {
			// Check queue mode — if a run is active this may queue, steer, or interrupt.
			if b.handleWithQueueMode(ctx, sessionKey, chatID, content) {
				b.sendQueuedAck(delivery.Chat)
				return nil
			}
		}

		// Send the lifecycle placeholder immediately (within ~0ms of receipt),
		// before the debounce window and any AI processing.
		canReuseAck := b.sendImmediateAck(delivery.Chat, msg.ID) != nil

		// Fragment buffering → debounce → async hub run.
		b.fragmentBuffer.TryBuffer(chatID, userID, msg.ID, content, func(assembled string) {
			b.debouncer.Debounce(chatID, assembled, func(combined string) {
				if err := b.handleCombinedChatTurn(ctx, c, sessionKey, combined, canReuseAck); err != nil {
					log.Printf("Failed to handle agent request: %v", err)
					c.Send("❌ Sorry, I encountered an error processing your request.") //nolint:errcheck
				}
			})
		})
		return nil
	}

	// No AI configured — echo the message.
	return c.Send(fmt.Sprintf("You said: %s", content))
}

// handleStreamingRequest processes message with streaming response
func (b *Bot) handleStreamingRequest(ctx context.Context, c telebot.Context, content, session string) error {
	// Build messages for AI
	messages := []ai.Message{
		{Role: "system", Content: b.personality.GetSystemPrompt()},
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

	// Get AI client for this session (with model override if set)
	aiClient := b.getAIClientForSession(c.Chat().ID)

	// Start streaming (requires StreamingClient)
	streamClient, ok := aiClient.(ai.StreamingClient)
	if !ok {
		// Fallback to non-streaming for providers without streaming support
		resp, err := aiClient.Complete(ctx, messages)
		if err != nil {
			b.api.Edit(thinkingMsg, "❌ Sorry, I encountered an error.")
			return err
		}
		b.api.Edit(thinkingMsg, resp)
		return nil
	}
	streamCh := streamClient.CompleteStream(ctx, messages)

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
	if err := b.memory.AppendToToday(fmt.Sprintf("Assistant: %s", finalContent)); err != nil {
		log.Printf("Failed to save to memory: %v", err)
	}

	// Save session
	if err := b.store.SaveSession(c.Chat().ID, finalContent); err != nil {
		log.Printf("Failed to save session: %v", err)
	}

	return nil
}

// getAIClientForModel returns an AI client configured for the given model.
// Returns the default client if model matches the configured default.
func (b *Bot) getAIClientForModel(model string) ai.Client {
	return b.getAIClientForModelAndThinkLevel(model, "")
}

// getAIClientForModelAndThinkLevel returns an AI client configured for the given
// model and thinking level. A non-empty thinkLevel is forwarded to the Anthropic
// client so it can use native extended thinking; other providers ignore it.
// When both model matches the default and thinkLevel is empty, the pre-configured
// client (which may include failover) is returned.
func (b *Bot) getAIClientForModelAndThinkLevel(model, thinkLevel string) ai.Client {
	if model == b.aiConfig.Model && thinkLevel == "" {
		return b.ai
	}

	cfg := ai.ProviderConfig{
		Name:       b.aiConfig.Provider,
		APIKey:     b.aiConfig.APIKey,
		Model:      model,
		BaseURL:    b.aiConfig.BaseURL,
		ThinkLevel: thinkLevel,
	}

	client, err := ai.NewClient(cfg)
	if err != nil {
		log.Printf("Failed to create AI client with model %s thinkLevel %s: %v", model, thinkLevel, err)
		return b.ai // Fallback to default
	}

	return client
}

// getEffectiveModel returns the model to use for a chat session

// getAIClientForSession returns an AI client with the effective model for the session.
// When the effective model is the default, the pre-configured client (which may include
// failover) is returned. Otherwise a plain client for the overridden model is created.
func (b *Bot) getAIClientForSession(chatID int64) ai.Client {
	effectiveModel := b.getEffectiveModel(chatID)

	// If model is the same as default, use the pre-configured client (may have failover).
	if effectiveModel == b.aiConfig.Model {
		if b.ai != nil {
			return b.ai
		}
		if b.streamingAI != nil {
			return b.streamingAI
		}
	}

	// Create a new client for the user-overridden model.
	cfg := ai.ProviderConfig{
		Name:    b.aiConfig.Provider,
		APIKey:  b.aiConfig.APIKey,
		Model:   effectiveModel,
		BaseURL: b.aiConfig.BaseURL,
	}

	client, err := ai.NewClient(cfg)
	if err != nil {
		log.Printf("Failed to create AI client with model %s: %v", effectiveModel, err)
		if b.ai != nil {
			return b.ai
		}
		return b.streamingAI
	}

	return client
}
func (b *Bot) getEffectiveModel(chatID int64) string {
	override, err := b.store.GetModelOverride(chatID)
	if err != nil {
		log.Printf("Failed to get model override: %v", err)
		return b.aiConfig.Model
	}

	if override != "" {
		return override
	}

	return b.aiConfig.Model
}

// resolveModelAlias resolves a short alias to a full model name.
// If no alias matches, the original name is returned unchanged.
func (b *Bot) resolveModelAlias(name string) string {
	aliases := b.aiConfig.ModelAliases
	if aliases == nil {
		aliases = config.DefaultModelAliases
	}
	if resolved, ok := aliases[strings.ToLower(name)]; ok {
		return resolved
	}
	return name
}

// getModelAliases returns the effective alias map (configured or defaults).
func (b *Bot) getModelAliases() map[string]string {
	if b.aiConfig.ModelAliases != nil {
		return b.aiConfig.ModelAliases
	}
	return config.DefaultModelAliases
}

// handleModelCommand handles the /model command
func (b *Bot) handleModelCommand(c telebot.Context) error {
	args := strings.TrimSpace(c.Message().Payload)
	chatID := c.Chat().ID

	// No arguments - show current model
	if args == "" {
		override, err := b.store.GetModelOverride(chatID)
		if err != nil {
			log.Printf("Failed to get model override: %v", err)
			return c.Send("❌ Failed to get current model")
		}

		if override != "" {
			return c.Send(fmt.Sprintf("🧠 Current model: `%s` (session override)\nDefault: `%s`",
				override, b.aiConfig.Model),
				&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
		}

		return c.Send(fmt.Sprintf("🧠 Current model: `%s` (default)", b.aiConfig.Model),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	// Handle "list" command
	if args == "list" {
		availableModels := ai.AvailableModels()

		var response strings.Builder
		response.WriteString("🧠 *Available Models:*\n\n")

		for provider, models := range availableModels {
			response.WriteString(fmt.Sprintf("*%s:*\n", strings.ToUpper(provider)))
			for _, model := range models {
				response.WriteString(fmt.Sprintf("• `%s`\n", model))
			}
			response.WriteString("\n")
		}

		// Show aliases
		aliases := b.getModelAliases()
		if len(aliases) > 0 {
			response.WriteString("*Aliases:*\n")
			for alias, fullName := range aliases {
				response.WriteString(fmt.Sprintf("• `%s` → `%s`\n", alias, fullName))
			}
			response.WriteString("\n")
		}

		response.WriteString("Usage: `/model <model-name>` or `/model <alias>` to set override")

		return c.Send(response.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	// Handle "clear" or "reset" command
	if args == "clear" || args == "reset" {
		if err := b.store.SetModelOverride(chatID, ""); err != nil {
			log.Printf("Failed to clear model override: %v", err)
			return c.Send("❌ Failed to clear model override")
		}
		return c.Send(fmt.Sprintf("✅ Model override cleared. Using default: `%s`", b.aiConfig.Model),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	// Set model override (resolve alias first)
	model := b.resolveModelAlias(args)
	if err := b.store.SetModelOverride(chatID, model); err != nil {
		log.Printf("Failed to set model override: %v", err)
		return c.Send("❌ Failed to set model override")
	}

	return c.Send(fmt.Sprintf("✅ Model override set to: `%s`\n\n⚠️ Note: Model will be used for this session only. Default model: `%s`",
		model, b.aiConfig.Model),
		&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// GetStore returns the underlying storage instance.
func (b *Bot) GetStore() *storage.Store { return b.store }

// GetAgentRegistry returns the agent registry.
func (b *Bot) GetAgentRegistry() *agent.AgentRegistry { return b.agentRegistry }

// GetScheduler returns the cron scheduler.
func (b *Bot) GetScheduler() tools.CronScheduler { return b.scheduler }

// GetStatus returns bot status information for API
func (b *Bot) GetStatus() map[string]interface{} {
	status := map[string]interface{}{
		"name":   b.personality.GetName(),
		"emoji":  b.personality.GetEmoji(),
		"status": "running",
	}

	if b.aiConfig.APIKey != "" {
		status["ai"] = map[string]string{
			"provider": b.aiConfig.Provider,
			"model":    b.aiConfig.Model,
		}
	}

	// Get session count from store
	// Note: This would require adding a method to storage to count sessions
	status["sessions"] = 0

	return status
}

// SendMessage sends a text message to a specific chat
func (b *Bot) SendMessage(chatID int64, text string) error {
	chat := &telebot.Chat{ID: chatID}
	_, err := b.api.Send(chat, text, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	return err
}

// AbortRun cancels the active run for chatID, if any.
// It tries both DM and group session keys.
func (b *Bot) AbortRun(chatID int64) error {
	b.hub.Cancel(agent.NewDMSessionKey(chatID))
	b.hub.Cancel(agent.NewGroupSessionKey(chatID))
	return nil
}

// RespondToApproval approves or rejects a pending approval by ID.
func (b *Bot) RespondToApproval(id string, approved bool) error {
	if b.approvalManager == nil {
		return fmt.Errorf("approval manager not initialised")
	}
	return b.approvalManager.HandleCallback(id, approved)
}

// SetModel overrides the model used for chatID.
func (b *Bot) SetModel(chatID int64, model string) error {
	return b.store.SetModelOverride(chatID, model)
}

// SetAgent switches the active agent for chatID.
func (b *Bot) SetAgent(chatID int64, agentName string) error {
	return b.store.SetActiveAgent(chatID, agentName)
}

// SetControlHub wires the control server event hub so the bot can push
// run, tool, and approval events to connected WebSocket clients.
// Must be called before the bot starts processing messages.
func (b *Bot) SetControlHub(h *control.Hub) {
	b.controlHub = h
	if b.approvalManager != nil {
		b.approvalManager.SetControlHub(h)
	}
}

// SubagentHub returns the runtime Hub used for sub-agent completion routing.
// May return nil before Start() has been called.
func (b *Bot) SubagentHub() *runtime.Hub {
	return b.subagentHub
}

// handleAuthCommand handles the /auth command (admin only)
func (b *Bot) handleAuthCommand(c telebot.Context) error {
	userID := c.Sender().ID

	// Check if user is admin
	if !b.authManager.IsAdmin(userID) {
		return c.Send("🔒 This command is only available to administrators.")
	}

	args := strings.Fields(c.Message().Payload)
	if len(args) == 0 {
		return c.Send(`🔐 *Auth Management Commands:*

/auth add <userID> - Authorize a user
/auth remove <userID> - Deauthorize a user
/auth list - List all authorized users
/auth pair - Generate a pairing code`, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	command := args[0]

	switch command {
	case "add":
		if len(args) < 2 {
			return c.Send("Usage: /auth add <userID>")
		}
		var targetUserID int64
		if _, err := fmt.Sscanf(args[1], "%d", &targetUserID); err != nil {
			return c.Send("❌ Invalid user ID format")
		}

		if err := b.authManager.AuthorizeUser(targetUserID, ""); err != nil {
			log.Printf("Failed to authorize user: %v", err)
			return c.Send("❌ Failed to authorize user")
		}

		return c.Send(fmt.Sprintf("✅ User `%d` has been authorized", targetUserID),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	case "remove":
		if len(args) < 2 {
			return c.Send("Usage: /auth remove <userID>")
		}
		var targetUserID int64
		if _, err := fmt.Sscanf(args[1], "%d", &targetUserID); err != nil {
			return c.Send("❌ Invalid user ID format")
		}

		if err := b.authManager.DeauthorizeUser(targetUserID); err != nil {
			log.Printf("Failed to deauthorize user: %v", err)
			return c.Send("❌ Failed to deauthorize user")
		}

		return c.Send(fmt.Sprintf("✅ User `%d` has been deauthorized", targetUserID),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	case "list":
		users, err := b.authManager.ListAuthorizedUsers()
		if err != nil {
			log.Printf("Failed to list authorized users: %v", err)
			return c.Send("❌ Failed to list authorized users")
		}

		if len(users) == 0 {
			return c.Send("📋 No authorized users found")
		}

		var response strings.Builder
		response.WriteString("📋 *Authorized Users:*\n\n")
		for _, user := range users {
			response.WriteString(fmt.Sprintf("• User ID: `%d`", user.UserID))
			if user.Username != "" {
				response.WriteString(fmt.Sprintf(" (@%s)", user.Username))
			}
			response.WriteString(fmt.Sprintf("\n  Method: %s\n  Authorized: %s\n\n",
				user.PairedBy, user.AuthorizedAt))
		}

		return c.Send(response.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	case "pair":
		code := b.authManager.GeneratePairingCode()
		return c.Send(fmt.Sprintf("🔑 *Pairing Code Generated:*\n\n`%s`\n\nThis code will expire in 5 minutes.\nUsers can pair using: `/pair %s`",
			code, code), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})

	default:
		return c.Send("❌ Unknown auth command. Use /auth without arguments for help.")
	}
}

// handlePairCommand handles the /pair command (any user)
func (b *Bot) handlePairCommand(c telebot.Context) error {
	userID := c.Sender().ID
	username := c.Sender().Username
	args := strings.TrimSpace(c.Message().Payload)

	if args == "" {
		return c.Send("Usage: /pair <code>\n\nEnter the 6-digit pairing code provided by the administrator.")
	}

	// Validate and authorize
	if b.authManager.ValidatePairingCode(args, userID, username) {
		return c.Send("✅ Successfully paired! You now have access to the bot.")
	}

	return c.Send("❌ Invalid or expired pairing code. Please request a new code from the administrator.")
}

// handleActivateCommand handles the /activate command
func (b *Bot) handleActivateCommand(c telebot.Context) error {
	chatID := c.Chat().ID

	// Only works in group chats
	if c.Chat().Type == telebot.ChatPrivate {
		return c.Send("This command only works in group chats.")
	}

	if err := b.groupManager.SetMode(chatID, ModeActive); err != nil {
		log.Printf("Failed to set active mode: %v", err)
		return c.Send("❌ Failed to activate bot")
	}

	return c.Send("✅ Bot activated! I'll respond to all messages in this group.")
}

// handleStandbyCommand handles the /standby command
func (b *Bot) handleStandbyCommand(c telebot.Context) error {
	chatID := c.Chat().ID

	// Only works in group chats
	if c.Chat().Type == telebot.ChatPrivate {
		return c.Send("This command only works in group chats.")
	}

	if err := b.groupManager.SetMode(chatID, ModeStandby); err != nil {
		log.Printf("Failed to set standby mode: %v", err)
		return c.Send("❌ Failed to set standby mode")
	}

	return c.Send("✅ Bot in standby mode. Mention me or reply to my messages to talk.")
}

// SetupApprovalHandlers registers callback handlers for approval buttons
func (b *Bot) SetupApprovalHandlers() {
	// Handle approve button
	b.api.Handle(telebot.OnCallback, func(c telebot.Context) error {
		callback := c.Callback()
		if callback == nil {
			return nil
		}

		// Parse callback data: "approve|<requestID>" or "deny|<requestID>"
		parts := strings.Split(callback.Data, "|")
		if len(parts) != 2 {
			return c.Respond(&telebot.CallbackResponse{Text: "Invalid callback data"})
		}

		action := parts[0]
		requestID := parts[1]

		approved := action == "approve"

		// Handle the callback
		if err := b.approvalManager.HandleCallback(requestID, approved); err != nil {
			return c.Respond(&telebot.CallbackResponse{Text: "Request not found or expired"})
		}

		// Update message to show decision
		var responseText string
		if approved {
			responseText = "✅ Command approved and executing..."
		} else {
			responseText = "❌ Command denied"
		}

		// Edit the message to remove buttons and show result
		c.Edit(responseText)

		return c.Respond(&telebot.CallbackResponse{})
	})
}

// GetApprovalFunc returns a function that can be used by tools to request approval
func (b *Bot) GetApprovalFunc(chatID int64) func(command string) (bool, error) {
	return func(command string) (bool, error) {
		// Check if command is dangerous
		if !b.approvalManager.IsDangerous(command) {
			return true, nil // Safe command, auto-approve
		}

		// Request user approval for dangerous command
		resultCh, _ := b.approvalManager.RequestApproval(chatID, command)

		// Wait for approval with timeout
		select {
		case approved := <-resultCh:
			return approved, nil
		case <-time.After(65 * time.Second):
			return false, fmt.Errorf("approval timeout")
		}
	}
}

// takeAckHandle retrieves and removes the pending lifecycle placeholder for chatID.
// It is the consume-once counterpart to sendImmediateAck.
func (b *Bot) takeAckHandle(chatID int64) *AckHandle {
	return b.ackManager.Take(chatID)
}

// updateAckStatus edits an existing lifecycle placeholder to the provided job state.
func (b *Bot) updateAckStatus(handle *AckHandle, status telegramJobStatus, detail string) {
	if handle == nil || handle.Message == nil {
		return
	}
	if _, err := b.api.Edit(handle.Message, formatTelegramJobStatus(handle.JobID, status, detail)); err != nil {
		log.Printf("[ack] failed to update placeholder for chat=%d job=%s: %v", handle.ChatID, handle.JobID, err)
	}
}

// sendImmediateAck sends a lifecycle placeholder immediately upon receiving an inbound
// Telegram message, before any debounce or AI processing.
// Only one tracked ack is created per chat — if one already exists the call is a no-op.
func (b *Bot) sendImmediateAck(chat *telebot.Chat, sourceMessageID int) *AckHandle {
	return b.sendAck(chat, newTelegramJobID(chat.ID, sourceMessageID), jobStatusAccepted, "")
}

// sendQueuedAck notifies the user that a message was received while another run
// is still active. Queued inputs may be merged by the debouncer, so this is a
// plain status note instead of a tracked per-job placeholder.
func (b *Bot) sendQueuedAck(chat *telebot.Chat) {
	if _, err := b.api.Send(chat, "⏳ Status: queued\nWaiting for the active run to finish."); err != nil {
		log.Printf("[ack] failed to send queued note for chat=%d: %v", chat.ID, err)
	}
}

// sendAck sends a tracked lifecycle placeholder message with typing indicator.
// Only one tracked ack is created per chat — if one already exists the call is a no-op.
func (b *Bot) sendAck(chat *telebot.Chat, jobID string, status telegramJobStatus, detail string) *AckHandle {
	chatID := chat.ID
	if b.ackManager.Exists(chatID) {
		return nil
	}

	// Typing indicator in parallel — satisfies "sendChatAction immediately" requirement
	go b.api.Notify(chat, telebot.Typing)

	// Send placeholder
	text := formatTelegramJobStatus(jobID, status, detail)
	ackMsg, err := b.api.Send(chat, text)
	if err != nil {
		log.Printf("[ack] failed to send placeholder for chat=%d: %v", chatID, err)
		return nil
	}

	handle := &AckHandle{Message: ackMsg, ChatID: chatID, JobID: jobID}
	b.ackManager.Set(chatID, handle)
	log.Printf("[ack] placeholder sent for chat=%d job=%s msg_id=%d text=%q", chatID, jobID, ackMsg.ID, text)
	return handle
}

// SetupLocalCommandApproval configures the LocalCommand tool with approval function
func (b *Bot) SetupLocalCommandApproval(chatID int64) {
	// Get the tool registry from toolAgent (we'd need to expose it)
	// For now, this is a placeholder that would be called before executing tools
	// The actual implementation would require modifying the tool agent to accept
	// per-request configuration
}
