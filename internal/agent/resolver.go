package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
	"ok-gobot/internal/delegation"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/tools"
)

// SessionStore provides session-scoped data needed for run resolution.
// Implemented by storage.Store.
type SessionStore interface {
	GetModelOverride(chatID int64) (string, error)
	GetActiveAgent(chatID int64) (string, error)
	GetSessionOption(chatID int64, key string) (string, error)
}

// AIResolverConfig holds AI provider configuration for creating clients.
type AIResolverConfig struct {
	Provider               string
	Model                  string
	APIKey                 string
	BaseURL                string
	ChatGPTAuthFile        string
	ChatGPTCodexHome       string
	ChatGPTCodexBinary     string
	DefaultThinking        string
	DefaultClient          ai.Client
	ModelAliases           map[string]string
	ModelTier              string
	BackendPreflight       func(context.Context, string, string, string) (ai.BackendHealth, error)
	BackendOutcomeReporter ai.BackendOutcomeReporter
	// Interaction fast lane: path-level defaults for runs flagged with
	// RunOverrides.UseInteraction (plain text chat replies). They sit UNDER
	// session /model and /think choices and never touch profiles that carry
	// their own model. Best-effort: a lane model failing preflight degrades
	// the run to the default lane instead of failing it.
	InteractionModel    string
	InteractionThinking string
	// MemoryMode controls how memory is injected into the system prompt.
	// Recognized values: "eager" (default), "retrieval_first", "startup_recent".
	MemoryMode string
}

// RunResolver resolves session parameters into agent run components.
// It is injected into RuntimeHub so the hub can own agent creation
// without external orchestration from the transport adapter.
type RunResolver struct {
	Store              SessionStore
	Registry           *AgentRegistry
	DefaultPersonality *Personality
	AIConfig           AIResolverConfig
	ToolRegistry       *tools.Registry
	Scheduler          tools.CronScheduler
	SubagentSubmitter  tools.SubagentSubmitter // injected after hub creation
	MediaSender        tools.MediaSender       // sends generated media into the current chat (implemented by the bot)
	HooksDir           string                  // path to hooks directory; empty = ~/.ok-gobot/hooks/
	Router             *ai.Router              // optional: task-type model router
	MemoryManager      *memory.MemoryManager   // optional: active recall context pack source
	MemoryPackBudget   memory.ContextPackBudget
}

// RunOverrides allows callers to explicitly override model/thinking level
// for a single run (e.g. /task --model sonnet --thinking high).
type RunOverrides struct {
	Model      string
	ThinkLevel string
	// TaskType hints at the type of work being performed so the model router can
	// select the most appropriate model. Ignored when Model is set explicitly.
	// Valid values: "vision", "summarize", "reasoning", "coding", "default".
	TaskType string
	// UseInteraction opts this run into the interaction fast lane: the
	// resolver applies AIConfig.InteractionModel/InteractionThinking as
	// path-level defaults UNDER session choices. It is a soft signal, not an
	// override — explicit Model/ThinkLevel and /model, /think always win.
	UseInteraction bool
	// TierModel/TierThinking carry cost-tier defaults for delegated runs.
	// Like the interaction lane they sit UNDER session choices — explicit
	// Model/ThinkLevel and /model, /think always win. Budgets travel on the
	// delegation.Job; only the model/thinking defaults ride here so the hub
	// never promotes them above user intent.
	TierModel    string
	TierThinking string
}

// RunComponents holds everything needed to execute a single agent run.
type RunComponents struct {
	Agent         *ToolCallingAgent
	Profile       *AgentProfile
	BackendHealth ai.BackendHealth
	Model         string
	ModelTier     string
	Effort        string
}

// Resolve creates the tool-calling agent and its dependencies for a chat session.
// isSubagent prevents injecting browser_task (avoids recursive subagent spawning).
func (r *RunResolver) Resolve(chatID int64, overrides *RunOverrides, job *delegation.Job, isSubagent ...bool) (*RunComponents, error) {
	return r.resolve(context.Background(), chatID, overrides, job, nil, isSubagent...)
}

func (r *RunResolver) resolve(ctx context.Context, chatID int64, overrides *RunOverrides, job *delegation.Job, recallCtx *memory.RecallContext, isSubagent ...bool) (*RunComponents, error) {
	profile := r.resolveProfile(chatID)
	model := r.resolveModel(chatID, profile, overrides)
	thinkLevel := r.resolveThinkLevel(chatID, profile, overrides)
	modelTier := r.resolveModelTier(profile, job, overrides)
	backendHealth, err := r.preflightBackend(ctx, model, modelTier, thinkLevel)
	if err != nil && overrides != nil && overrides.UseInteraction {
		// The fast lane is best-effort: if its model fails preflight (typo,
		// retired id, backend hiccup), degrade to the default lane instead
		// of failing every light reply.
		log.Printf("[resolver] interaction fast lane failed preflight, degrading to the default lane: %v", err)
		trimmed := *overrides
		trimmed.UseInteraction = false
		overrides = &trimmed
		model = r.resolveModel(chatID, profile, overrides)
		thinkLevel = r.resolveThinkLevel(chatID, profile, overrides)
		backendHealth, err = r.preflightBackend(ctx, model, modelTier, thinkLevel)
	}
	if err != nil {
		return nil, err
	}
	if backendHealth.Identity.Model != "" && backendHealth.Identity.Model != model {
		log.Printf("[resolver] backend fallback selected model=%s instead of requested=%s reason=%s", backendHealth.Identity.Model, model, backendHealth.Fallback.Reason)
		model = backendHealth.Identity.Model
	}
	aiClient := r.buildAIClient(model, modelTier, thinkLevel)
	sub := len(isSubagent) > 0 && isSubagent[0]
	memoryPolicy := r.buildMemoryRecallPolicy(chatID, profile, job, recallCtx)
	toolReg := r.buildToolRegistryWithMemoryPolicy(chatID, profile, sub, job, memoryPolicy)

	aliases := r.AIConfig.ModelAliases
	if aliases == nil {
		aliases = config.DefaultModelAliases
	}

	ta := NewToolCallingAgent(aiClient, toolReg, profile.Personality)
	ta.SetModel(model)
	ta.SetModelAliases(aliases)
	if memoryPolicy != nil {
		ta.SetMemoryRecallPolicy(memoryPolicy)
	}
	if thinkLevel != "" {
		ta.SetThinkLevel(thinkLevel)
	}
	if r.AIConfig.MemoryMode != "" {
		ta.SetMemoryMode(r.AIConfig.MemoryMode)
	}
	ta.SetHookRunner(NewHookRunner(r.HooksDir))

	return &RunComponents{Agent: ta, Profile: profile, BackendHealth: backendHealth, Model: model, ModelTier: modelTier, Effort: thinkLevel}, nil
}

func (r *RunResolver) preflightBackend(ctx context.Context, model, tier, effort string) (ai.BackendHealth, error) {
	if r == nil || r.AIConfig.BackendPreflight == nil {
		return ai.BackendHealth{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	health, err := r.AIConfig.BackendPreflight(ctx, model, tier, effort)
	identity := health.Identity.String()
	if identity == "" {
		identity = fmt.Sprintf("provider=%s model=%s tier=%s effort=%s", r.AIConfig.Provider, model, tier, effort)
	}
	decision := health.Fallback
	log.Printf("[resolver] backend preflight: %s health=%s fallback_action=%s fallback_to=%s reason=%s", identity, health.Status, decision.Action, decision.ToModel, decision.Reason)
	if err != nil {
		return health, fmt.Errorf("backend preflight blocked session start: %w", err)
	}
	return health, nil
}

func (r *RunResolver) resolveModelTier(profile *AgentProfile, job *delegation.Job, overrides *RunOverrides) string {
	if job != nil && strings.TrimSpace(job.Model) != "" {
		return "job"
	}
	if overrides != nil && strings.TrimSpace(overrides.Model) != "" {
		return "override"
	}
	if profile != nil && strings.TrimSpace(profile.Model) != "" {
		return "agent"
	}
	if strings.TrimSpace(r.AIConfig.ModelTier) != "" {
		return strings.TrimSpace(r.AIConfig.ModelTier)
	}
	return "default"
}

func (r *RunResolver) buildMemoryRecallPolicy(chatID int64, profile *AgentProfile, job *delegation.Job, recallCtx *memory.RecallContext) *memory.RecallPolicy {
	if recallCtx == nil && chatID == 0 {
		return nil
	}

	ctx := memory.RecallContext{ChatID: chatID}
	if recallCtx != nil {
		ctx = *recallCtx
		if ctx.ChatID == 0 {
			ctx.ChatID = chatID
		}
	}
	if ctx.RoleName == "" && profile != nil {
		ctx.RoleName = profile.Name
	}
	return memory.NewRecallPolicy(ctx)
}

func (r *RunResolver) resolveProfile(chatID int64) *AgentProfile {
	if r.Registry == nil {
		return &AgentProfile{
			Name:         "default",
			Personality:  r.DefaultPersonality,
			Model:        r.AIConfig.Model,
			AllowedTools: []string{},
		}
	}

	agentName, err := r.Store.GetActiveAgent(chatID)
	if err != nil {
		log.Printf("[resolver] failed to get active agent for chat %d: %v", chatID, err)
		return r.Registry.Default()
	}

	profile := r.Registry.Get(agentName)
	if profile == nil {
		log.Printf("[resolver] agent '%s' not found, using default", agentName)
		return r.Registry.Default()
	}

	return profile
}

func (r *RunResolver) resolveModel(chatID int64, profile *AgentProfile, overrides *RunOverrides) string {
	// Explicit model override has highest priority — user intent is unambiguous.
	if overrides != nil && overrides.Model != "" {
		return overrides.Model
	}

	// Soft defaults (cost tier, interaction lane) sit under the session
	// choice and are all disabled when the session state cannot be read —
	// a possibly-pinned session must never lose to a default.
	softOK := overrides != nil

	// Session-level model override (set via /model command).
	if chatID != 0 {
		override, err := r.Store.GetModelOverride(chatID)
		if err == nil && override != "" {
			return override
		}
		if err != nil {
			softOK = false
		}
	}

	// Cost-tier default for delegated runs.
	if softOK && overrides.TierModel != "" {
		return overrides.TierModel
	}

	// Interaction fast lane: a path-level default for light replies, applied
	// under the session choice. Profiles with their own model keep priority.
	if softOK && overrides.UseInteraction {
		if lane := r.interactionLaneModel(profile); lane != "" {
			return lane
		}
	}

	// Task-type routing: select model based on the kind of work being performed.
	// Only applies when a task type is provided and the router has routes configured.
	if overrides != nil && overrides.TaskType != "" && r.Router != nil && r.Router.HasRoutes() {
		model, reason := r.Router.Route(ai.TaskType(overrides.TaskType))
		log.Printf("[resolver] task-type routing: task_type=%s model=%s reason=%s", overrides.TaskType, model, reason)
		return model
	}

	// Agent profile model.
	if profile.Model != "" {
		return profile.Model
	}

	// Global default.
	return r.AIConfig.Model
}

// interactionLaneModel returns the alias-resolved fast-lane model, or ""
// when the lane is unset or the profile carries its own specialized model.
func (r *RunResolver) interactionLaneModel(profile *AgentProfile) string {
	lane := strings.TrimSpace(r.AIConfig.InteractionModel)
	if lane == "" {
		return ""
	}
	if profile != nil && profile.Model != "" && profile.Model != r.AIConfig.Model {
		return ""
	}
	aliases := r.AIConfig.ModelAliases
	if aliases == nil {
		aliases = config.DefaultModelAliases
	}
	if resolved, ok := aliases[strings.ToLower(lane)]; ok {
		return resolved
	}
	return lane
}

// laneAppliesTo reports whether the interaction fast lane may touch this
// profile at all (profiles with a specialized model own their settings).
func (r *RunResolver) laneAppliesTo(profile *AgentProfile) bool {
	return profile == nil || profile.Model == "" || profile.Model == r.AIConfig.Model
}

func (r *RunResolver) resolveThinkLevel(chatID int64, profile *AgentProfile, overrides *RunOverrides) string {
	if overrides != nil && overrides.ThinkLevel != "" {
		return overrides.ThinkLevel
	}

	// Soft defaults share one guard: unreadable session state disables them.
	softOK := overrides != nil

	if chatID != 0 {
		level, err := r.Store.GetSessionOption(chatID, "think_level")
		if err != nil {
			softOK = false
		} else if level != "" {
			return level
		}
	}

	if softOK && overrides.TierThinking != "" {
		return overrides.TierThinking
	}

	if softOK && overrides.UseInteraction && r.laneAppliesTo(profile) {
		if lane := strings.TrimSpace(r.AIConfig.InteractionThinking); lane != "" {
			return lane
		}
	}

	return r.AIConfig.DefaultThinking
}

func (r *RunResolver) buildAIClient(model, modelTier, thinkLevel string) ai.Client {
	if model == r.AIConfig.Model && thinkLevel == r.AIConfig.DefaultThinking {
		return r.AIConfig.DefaultClient
	}

	cfg := ai.ProviderConfig{
		Name:               r.AIConfig.Provider,
		APIKey:             r.AIConfig.APIKey,
		Model:              model,
		BaseURL:            r.AIConfig.BaseURL,
		ThinkLevel:         thinkLevel,
		ChatGPTAuthFile:    r.AIConfig.ChatGPTAuthFile,
		ChatGPTCodexHome:   r.AIConfig.ChatGPTCodexHome,
		ChatGPTCodexBinary: r.AIConfig.ChatGPTCodexBinary,
	}

	client, err := ai.NewClient(cfg)
	if err != nil {
		log.Printf("[resolver] failed to create AI client for model=%s thinkLevel=%s: %v", model, thinkLevel, err)
		return r.AIConfig.DefaultClient
	}

	return ai.TrackClient(client, ai.BackendIdentity{
		Provider: r.AIConfig.Provider,
		Backend:  r.AIConfig.Provider,
		Model:    model,
		Tier:     modelTier,
		Effort:   thinkLevel,
		BaseURL:  r.AIConfig.BaseURL,
	}, r.AIConfig.BackendOutcomeReporter)
}

func (r *RunResolver) buildToolRegistry(chatID int64, profile *AgentProfile, isSubagent bool, job *delegation.Job) *tools.Registry {
	return r.buildToolRegistryWithMemoryPolicy(chatID, profile, isSubagent, job, nil)
}

func (r *RunResolver) buildToolRegistryWithMemoryPolicy(chatID int64, profile *AgentProfile, isSubagent bool, job *delegation.Job, memoryPolicy *memory.RecallPolicy) *tools.Registry {
	base := r.ToolRegistry

	// Filter by agent's allowed tools.
	if profile.HasToolRestrictions() {
		filtered := base.Child()
		for _, tool := range base.List() {
			if profile.IsToolAllowed(tool.Name()) {
				filtered.Register(tool)
			}
		}
		base = filtered
	}

	// Inject per-chat tools that need the chatID.
	// Main agents get browser_task instead of browser (to force subagent isolation).
	// Subagents get browser directly (no browser_task to prevent recursive spawning).
	// ChatScoped tools (e.g. image_gen) are rebound so their output reaches the chat.
	bindChat := r.MediaSender != nil && chatID != 0
	needsPerChat := (r.Scheduler != nil && chatID != 0) || (!isSubagent && r.SubagentSubmitter != nil && chatID != 0) || bindChat
	if needsPerChat {
		chatRegistry := base.Child()
		for _, tool := range base.List() {
			switch tool.Name() {
			case "cron", "browser_task", "host_task":
				// Re-injected below with chatID binding.
				continue
			case "browser":
				if !isSubagent {
					// Main agent must use browser_task, not browser directly.
					continue
				}
			case "exec":
				if !isSubagent {
					// SOUL.md: the main agent never executes. It delegates host ops
					// via host_task; only sub-agents get exec (chatID-bound below).
					continue
				}
			}
			if bindChat {
				if cs, ok := tools.AsChatScoped(tool); ok {
					chatRegistry.Register(cs.BindChat(r.MediaSender, chatID))
					continue
				}
			}
			chatRegistry.Register(tool)
		}
		if r.Scheduler != nil && chatID != 0 {
			chatRegistry.Register(tools.NewCronTool(r.Scheduler, chatID))
		}
		if !isSubagent && r.SubagentSubmitter != nil && chatID != 0 {
			chatRegistry.Register(tools.NewBrowserTaskTool(r.SubagentSubmitter, chatID))
			chatRegistry.Register(tools.NewHostTaskTool(r.SubagentSubmitter, chatID))
		}
		base = chatRegistry
	}

	if job != nil && len(job.ToolAllowlist) > 0 {
		filtered := base.Child()
		allowed := make(map[string]struct{}, len(job.ToolAllowlist))
		for _, name := range job.ToolAllowlist {
			allowed[name] = struct{}{}
		}
		for _, tool := range base.List() {
			if _, ok := allowed[tool.Name()]; ok {
				filtered.Register(tool)
			}
		}
		base = filtered
	}

	if memoryPolicy != nil {
		base = tools.ApplyMemoryRecallPolicy(base, memoryPolicy)
	}

	// Apply capability policy last so it covers all tools including
	// per-chat injected ones (cron, browser_task) and job-filtered ones.
	if profile.Policy != nil {
		base = tools.ApplyPolicy(base, profile.Policy)
	}

	return base
}
