package agent

import (
	"log"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
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
	Provider        string
	Model           string
	APIKey          string
	BaseURL         string
	DefaultThinking string
	DefaultClient   ai.Client
	ModelAliases    map[string]string
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
}

// RunOverrides allows callers to explicitly override agent/model/thinking level
// for a single run (e.g. /task --agent smart --model sonnet --thinking high).
type RunOverrides struct {
	AgentName  string
	Model      string
	ThinkLevel string
}

// RunComponents holds everything needed to execute a single agent run.
type RunComponents struct {
	Agent   *ToolCallingAgent
	Profile *AgentProfile
}

// Resolve creates the tool-calling agent and its dependencies for a chat session.
// The variadic isSubagent argument is retained for compatibility with legacy call
// sites, but the main runtime no longer alters tool composition around subagents.
func (r *RunResolver) Resolve(chatID int64, overrides *RunOverrides, isSubagent ...bool) (*RunComponents, error) {
	profile := r.resolveProfile(chatID, overrides)
	model := r.resolveModel(chatID, profile, overrides)
	thinkLevel := r.resolveThinkLevel(chatID, overrides)
	aiClient := r.buildAIClient(model, thinkLevel, profile)
	_ = isSubagent
	toolReg := r.buildToolRegistry(chatID, profile)

	aliases := r.AIConfig.ModelAliases
	if aliases == nil {
		aliases = config.DefaultModelAliases
	}

	ta := NewToolCallingAgent(aiClient, toolReg, profile.Personality)
	ta.SetModelAliases(aliases)
	if thinkLevel != "" {
		ta.SetThinkLevel(thinkLevel)
	}

	return &RunComponents{Agent: ta, Profile: profile}, nil
}

func (r *RunResolver) resolveProfile(chatID int64, overrides *RunOverrides) *AgentProfile {
	if r.Registry == nil {
		return &AgentProfile{
			Name:         "default",
			Personality:  r.DefaultPersonality,
			Model:        r.AIConfig.Model,
			AllowedTools: []string{},
		}
	}

	if overrides != nil && overrides.AgentName != "" {
		if profile := r.Registry.Get(overrides.AgentName); profile != nil {
			return profile
		}
		log.Printf("[resolver] override agent '%s' not found, using active/default profile", overrides.AgentName)
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
	// Explicit override has highest priority.
	if overrides != nil && overrides.Model != "" {
		return overrides.Model
	}

	// Session-level model override.
	if chatID != 0 {
		override, err := r.Store.GetModelOverride(chatID)
		if err == nil && override != "" {
			return override
		}
	}

	// Agent profile model.
	if profile.Model != "" {
		return profile.Model
	}

	// Global default.
	return r.AIConfig.Model
}

func (r *RunResolver) resolveThinkLevel(chatID int64, overrides *RunOverrides) string {
	if overrides != nil && overrides.ThinkLevel != "" {
		return overrides.ThinkLevel
	}

	if chatID != 0 {
		level, _ := r.Store.GetSessionOption(chatID, "think_level")
		if level != "" {
			return level
		}
	}

	return r.AIConfig.DefaultThinking
}

func (r *RunResolver) buildAIClient(model, thinkLevel string, profile *AgentProfile) ai.Client {
	hasAgentOverride := profile.Provider != "" || profile.BaseURL != "" || profile.APIKey != ""

	if !hasAgentOverride && model == r.AIConfig.Model && thinkLevel == "" {
		return r.AIConfig.DefaultClient
	}

	provider := r.AIConfig.Provider
	apiKey := r.AIConfig.APIKey
	baseURL := r.AIConfig.BaseURL

	if profile.Provider != "" {
		provider = profile.Provider
	}
	if profile.APIKey != "" {
		apiKey = profile.APIKey
	}
	if profile.BaseURL != "" {
		baseURL = profile.BaseURL
	}

	cfg := ai.ProviderConfig{
		Name:       provider,
		APIKey:     apiKey,
		Model:      model,
		BaseURL:    baseURL,
		ThinkLevel: thinkLevel,
	}

	client, err := ai.NewClient(cfg)
	if err != nil {
		log.Printf("[resolver] failed to create AI client for provider=%s model=%s thinkLevel=%s: %v", provider, model, thinkLevel, err)
		return r.AIConfig.DefaultClient
	}

	return client
}

func (r *RunResolver) buildToolRegistry(chatID int64, profile *AgentProfile) *tools.Registry {
	base := r.ToolRegistry

	// Filter by agent's allowed tools.
	if profile.HasToolRestrictions() {
		filtered := tools.NewRegistry()
		for _, tool := range base.List() {
			if profile.IsToolAllowed(tool.Name()) {
				filtered.Register(tool)
			}
		}
		base = filtered
	}

	// Inject only tools that genuinely require the active chat context.
	if r.Scheduler != nil && chatID != 0 {
		chatRegistry := tools.NewRegistry()
		for _, tool := range base.List() {
			switch tool.Name() {
			case "cron":
				continue
			}
			chatRegistry.Register(tool)
		}
		chatRegistry.Register(tools.NewCronTool(r.Scheduler, chatID))
		return chatRegistry
	}

	return base
}
