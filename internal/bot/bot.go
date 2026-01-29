package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
	"ok-gobot/internal/logger"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

// Bot wraps the Telegram bot with business logic
type Bot struct {
	api             *telebot.Bot
	store           *storage.Store
	ai              ai.Client
	streamingAI     *ai.OpenAICompatibleClient
	aiConfig        AIConfig
	personality     *agent.Personality
	agentRegistry   *agent.AgentRegistry
	toolRegistry    *tools.Registry
	safety          *agent.Safety
	memory          *agent.Memory
	toolAgent       *agent.ToolCallingAgent
	authManager     *AuthManager
	groupManager    *GroupManager
	approvalManager *ApprovalManager
	adminID         int64
	enableStream    bool
	debouncer       *Debouncer
	rateLimiter     *RateLimiter
	configWatcher   ConfigWatcher
	usageTracker    *UsageTracker
	activeRuns      map[int64]context.CancelFunc
	cancelMu        sync.Mutex
	fragmentBuffer  *FragmentBuffer
	mediaGroupBuf   *MediaGroupBuffer
	queueManager    *QueueManager
}

// AIConfig holds AI configuration for status display
type AIConfig struct {
	Provider     string
	Model        string
	APIKey       string
	BaseURL      string
	ModelAliases map[string]string
}

// newToolAgentWithAliases creates a ToolCallingAgent and configures model aliases.
func newToolAgentWithAliases(aiClient ai.Client, toolRegistry *tools.Registry, personality *agent.Personality, aliases map[string]string) *agent.ToolCallingAgent {
	ta := agent.NewToolCallingAgent(aiClient, toolRegistry, personality)
	if aliases != nil {
		ta.SetModelAliases(aliases)
	} else {
		ta.SetModelAliases(config.DefaultModelAliases)
	}
	return ta
}

// New creates a new bot instance
func New(token string, store *storage.Store, aiClient ai.Client, aiCfg AIConfig, personality *agent.Personality, agentRegistry *agent.AgentRegistry, authCfg config.AuthConfig, groupsCfg config.GroupsConfig, ttsCfg config.TTSConfig) (*Bot, error) {
	pref := telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}

	api, err := telebot.NewBot(pref)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot: %w", err)
	}

	// Create tool registry with TTS configuration
	toolsConfig := &tools.ToolsConfig{
		OpenAIAPIKey: aiCfg.APIKey,
		TTSProvider:  ttsCfg.Provider,
		TTSVoice:     ttsCfg.DefaultVoice,
	}
	toolRegistry, _ := tools.LoadFromConfigWithOptions("", toolsConfig)

	// Try to cast aiClient to streaming client
	streamingClient, _ := aiClient.(*ai.OpenAICompatibleClient)

	// Create auth manager
	authManager := NewAuthManager(store, authCfg)

	// Create group manager
	groupManager := NewGroupManager(store, groupsCfg.DefaultMode, api.Me.Username)

	return &Bot{
		api:           api,
		store:         store,
		ai:            aiClient,
		streamingAI:   streamingClient,
		aiConfig:      aiCfg,
		personality:   personality,
		agentRegistry: agentRegistry,
		toolRegistry:  toolRegistry,
		safety:        agent.NewSafety(),
		memory:        agent.NewMemory(""),
		toolAgent:     newToolAgentWithAliases(aiClient, toolRegistry, personality, aiCfg.ModelAliases),
		authManager:   authManager,
		groupManager:  groupManager,
		enableStream:  streamingClient != nil,
		debouncer:     NewDebouncer(1500 * time.Millisecond),
		rateLimiter:   NewRateLimiter(10, 1*time.Minute),
		usageTracker:   NewUsageTracker(),
		activeRuns:     make(map[int64]context.CancelFunc),
		fragmentBuffer: NewFragmentBuffer(),
		mediaGroupBuf:  NewMediaGroupBuffer(),
		queueManager:   NewQueueManager(),
	}, nil
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
		{Text: "clear", Description: "Clear conversation history"},
		{Text: "stop", Description: "Stop the current run"},
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
	b.api.Handle("/start", func(c telebot.Context) error {
		greeting := fmt.Sprintf("🦞 Welcome! I'm %s %s\n\n%s",
			name,
			emoji,
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
/tools - List available tools
/model - Manage AI model (list/set/clear)
/agent - Manage agents (list/switch)
/auth - Authorization management (admin only)
/pair <code> - Pair with bot using pairing code
/reload - Reload configuration (admin only)`, name)
		return c.Send(help)
	})

	b.api.Handle("/status", func(c telebot.Context) error {
		return b.handleStatusCommand(c)
	})

	b.api.Handle("/tools", func(c telebot.Context) error {
		toolsList := b.toolAgent.GetAvailableTools()
		return c.Send(fmt.Sprintf("🔧 Available Tools:\n\n%s", strings.Join(toolsList, "\n")))
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

	b.api.Handle("/model", func(c telebot.Context) error {
		return b.handleModelCommand(c)
	})


	b.api.Handle("/activate", func(c telebot.Context) error {
		return b.handleActivateCommand(c)
	})

	b.api.Handle("/standby", func(c telebot.Context) error {
		return b.handleStandbyCommand(c)
	})

	b.api.Handle("/auth", func(c telebot.Context) error {
		return b.handleAuthCommand(c)
	})

	b.api.Handle("/pair", func(c telebot.Context) error {
		return b.handlePairCommand(c)
	})

	b.api.Handle("/reload", func(c telebot.Context) error {
		return b.handleReloadCommand(c)
	})

	// Start bot in goroutine
	go b.api.Start()

	// Start heartbeat in background
	go b.startHeartbeat()

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

	// Check authorization first (skip for /pair command)
	if !strings.HasPrefix(content, "/pair") && !b.authManager.CheckAccess(userID, chatID) {
		logger.Debugf("Bot: auth denied for user=%d chat=%d", userID, chatID)
		return c.Send("🔒 Not authorized. Please contact the bot administrator.")
	}

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

	// Check if bot should respond in groups
	if !b.groupManager.ShouldRespond(chatID, msg, b.api.Me.Username) {
		logger.Debugf("Bot: skipping message in group chat=%d (standby)", chatID)
		return nil // Ignore message in standby mode without mention
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

	// Use ToolCallingAgent to process the request
	if b.ai != nil && !strings.HasPrefix(content, "/") {
		// Check queue mode - if a run is active, may queue/steer/interrupt
		if b.handleWithQueueMode(ctx, chatID, content) {
			return nil // Message was queued or steered
		}

		// Fragment buffering -> debounce -> process
		b.fragmentBuffer.TryBuffer(chatID, userID, msg.ID, content, func(assembled string) {
			b.debouncer.Debounce(chatID, assembled, func(combined string) {
				// Mark run as active
				b.queueManager.StartRun(chatID)
				defer func() {
					// Process any queued messages after run completes
					queued := b.queueManager.EndRun(chatID)
					if len(queued) > 0 {
						logger.Debugf("Bot: processing %d queued messages for chat=%d", len(queued), chatID)
						for _, qMsg := range queued {
							b.debouncer.Debounce(chatID, qMsg, func(qCombined string) {
								session, _ := b.store.GetSession(chatID)
								b.handleAgentRequest(ctx, c, qCombined, session)
							})
						}
					}
				}()

				session, err := b.store.GetSession(chatID)
				if err != nil {
					log.Printf("Failed to get session: %v", err)
				}

				if err := b.handleAgentRequest(ctx, c, combined, session); err != nil {
					log.Printf("Failed to handle agent request: %v", err)
					c.Send("❌ Sorry, I encountered an error processing your request.")
				}
			})
		})
		return nil
	}

	// Simple echo response for now
	return c.Send(fmt.Sprintf("You said: %s", content))
}

// handleAgentRequest processes message through the ToolCallingAgent
func (b *Bot) handleAgentRequest(ctx context.Context, c telebot.Context, content, session string) error {
	chatID := c.Chat().ID

	// Register cancellable context for /stop support
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.cancelMu.Lock()
	b.activeRuns[chatID] = cancel
	b.cancelMu.Unlock()
	defer func() {
		b.cancelMu.Lock()
		delete(b.activeRuns, chatID)
		b.cancelMu.Unlock()
	}()
	_ = runCtx // used below

	// Use agent-aware handlers if agent registry is configured
	if b.agentRegistry != nil {
		// Always use tool-calling path — streaming doesn't support tools
		return b.handleAgentRequestWithProfile(ctx, c, content, session)
	}

	// Legacy behavior for non-agent mode
	// Start typing indicator
	stopTyping := NewTypingIndicator(b.api, c.Chat())
	defer stopTyping()

	// Always use tool-calling path — streaming doesn't support tools
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

// getEffectiveModel returns the model to use for a chat session

// getAIClientForSession returns an AI client with the effective model for the session
func (b *Bot) getAIClientForSession(chatID int64) ai.Client {
	effectiveModel := b.getEffectiveModel(chatID)

	// If model is the same as default, return existing client
	if effectiveModel == b.aiConfig.Model && b.streamingAI != nil {
		return b.streamingAI
	}

	// Create a new client with the overridden model
	cfg := ai.ProviderConfig{
		Name:    b.aiConfig.Provider,
		APIKey:  b.aiConfig.APIKey,
		Model:   effectiveModel,
		BaseURL: b.aiConfig.BaseURL,
	}

	client, err := ai.NewClient(cfg)
	if err != nil {
		log.Printf("Failed to create AI client with model %s: %v", effectiveModel, err)
		return b.streamingAI // Fallback to default
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

// SetupLocalCommandApproval configures the LocalCommand tool with approval function
func (b *Bot) SetupLocalCommandApproval(chatID int64) {
	// Get the tool registry from toolAgent (we'd need to expose it)
	// For now, this is a placeholder that would be called before executing tools
	// The actual implementation would require modifying the tool agent to accept
	// per-request configuration
}
