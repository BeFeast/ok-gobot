package agent

import (
	"log"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
	"ok-gobot/internal/delegation"
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
	SubagentSubmitter  tools.SubagentSubmitter // injected after hub creation
}

// RunOverrides allows callers to explicitly override model/thinking level
// for a single run (e.g. /task --model sonnet --thinking high).
type RunOverrides struct {
	Model      string
	ThinkLevel string
}

// RunComponents holds everything needed to execute a single agent run.
type RunComponents struct {
	Agent   *ToolCallingAgent
	Profile *AgentProfile
}

// Resolve creates the tool-calling agent and its dependencies for a chat session.
// isSubagent prevents injecting browser_task (avoids recursive subagent spawning).
func (r *RunResolver) Resolve(chatID int64, overrides *RunOverrides, job *delegation.Job, isSubagent ...bool) (*RunComponents, error) {
	profile := r.resolveProfile(chatID)
	model := r.resolveModel(chatID, profile, overrides)
	thinkLevel := r.resolveThinkLevel(chatID, overrides)
	aiClient := r.buildAIClient(model, thinkLevel)
	sub := len(isSubagent) > 0 && isSubagent[0]
	toolReg := r.buildToolRegistry(chatID, profile, sub, job)

	aliases := r.AIConfig.ModelAliases
	if aliases == nil {
		aliases = config.DefaultModelAliases
	}

	ta := NewToolCallingAgent(aiClient, toolReg, profile.Personality)
	ta.SetModel(model)
	ta.SetModelAliases(aliases)
	if thinkLevel != "" {
		ta.SetThinkLevel(thinkLevel)
	}

	return &RunComponents{Agent: ta, Profile: profile}, nil
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

func (r *RunResolver) buildAIClient(model, thinkLevel string) ai.Client {
	if model == r.AIConfig.Model && thinkLevel == "" {
		return r.AIConfig.DefaultClient
	}

	cfg := ai.ProviderConfig{
		Name:       r.AIConfig.Provider,
		APIKey:     r.AIConfig.APIKey,
		Model:      model,
		BaseURL:    r.AIConfig.BaseURL,
		ThinkLevel: thinkLevel,
	}

	client, err := ai.NewClient(cfg)
	if err != nil {
		log.Printf("[resolver] failed to create AI client for model=%s thinkLevel=%s: %v", model, thinkLevel, err)
		return r.AIConfig.DefaultClient
	}

	return client
}

func (r *RunResolver) buildToolRegistry(chatID int64, profile *AgentProfile, isSubagent bool, job *delegation.Job) *tools.Registry {
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

	// Inject per-chat tools (cron, browser_task) that need the chatID.
	// Main agents get browser_task instead of browser (to force subagent isolation).
	// Subagents get browser directly (no browser_task to prevent recursive spawning).
	needsPerChat := (r.Scheduler != nil && chatID != 0) || (!isSubagent && r.SubagentSubmitter != nil && chatID != 0)
	if needsPerChat {
		chatRegistry := base.Child()
		for _, tool := range base.List() {
			switch tool.Name() {
			case "cron", "browser_task":
				// Re-injected below with chatID binding.
				continue
			case "browser":
				if !isSubagent {
					// Main agent must use browser_task, not browser directly.
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

	return base
}
