package agent

import (
	"fmt"
	"log"

	"ok-gobot/internal/config"
	"ok-gobot/internal/tools"
)

// AgentProfile holds an agent's configuration and personality
type AgentProfile struct {
	Name         string
	Personality  *Personality
	Model        string
	AllowedTools []string
	Policy       *tools.CapabilityPolicy // nil = fully permissive (backward compatible)
}

// AgentRegistry manages multiple agent profiles
type AgentRegistry struct {
	agents       map[string]*AgentProfile
	defaultAgent string
}

// NewAgentRegistry creates a new agent registry from configuration
func NewAgentRegistry(configs []config.AgentConfig, globalModel string, globalSoulPath string) (*AgentRegistry, error) {
	registry := &AgentRegistry{
		agents:       make(map[string]*AgentProfile),
		defaultAgent: "default",
	}

	// If no agents configured, create a default agent
	if len(configs) == 0 {
		log.Println("🤖 No agents configured, creating default agent")
		personality, err := NewPersonality(globalSoulPath)
		if err != nil {
			log.Printf("⚠️ Failed to load default personality: %v", err)
			personality = &Personality{}
		}

		registry.agents["default"] = &AgentProfile{
			Name:         "default",
			Personality:  personality,
			Model:        globalModel,
			AllowedTools: []string{}, // Empty = all tools allowed
		}
		return registry, nil
	}

	// Load each configured agent
	for _, cfg := range configs {
		if cfg.Name == "" {
			return nil, fmt.Errorf("agent name cannot be empty")
		}

		if cfg.SoulPath == "" {
			return nil, fmt.Errorf("agent %s: soul_path cannot be empty", cfg.Name)
		}

		log.Printf("🤖 Loading agent '%s' from %s...", cfg.Name, cfg.SoulPath)
		personality, err := NewPersonality(cfg.SoulPath)
		if err != nil {
			log.Printf("⚠️ Failed to load personality for agent '%s': %v", cfg.Name, err)
			personality = &Personality{}
		}

		// Use global model if agent doesn't specify one
		model := cfg.Model
		if model == "" {
			model = globalModel
		}

		registry.agents[cfg.Name] = &AgentProfile{
			Name:         cfg.Name,
			Personality:  personality,
			Model:        model,
			AllowedTools: cfg.AllowedTools,
			Policy:       resolveCapabilityPolicy(cfg.Capabilities),
		}

		log.Printf("✅ Agent '%s' loaded (model: %s)", cfg.Name, model)
	}

	// Set first agent as default if "default" doesn't exist
	if _, ok := registry.agents["default"]; !ok && len(configs) > 0 {
		registry.defaultAgent = configs[0].Name
	}

	return registry, nil
}

// Get returns an agent profile by name
func (r *AgentRegistry) Get(name string) *AgentProfile {
	if profile, ok := r.agents[name]; ok {
		return profile
	}
	return nil
}

// List returns all agent names
func (r *AgentRegistry) List() []string {
	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	return names
}

// Default returns the default agent profile
func (r *AgentRegistry) Default() *AgentProfile {
	if profile, ok := r.agents[r.defaultAgent]; ok {
		return profile
	}
	// Fallback to any agent if default is missing
	for _, profile := range r.agents {
		return profile
	}
	return nil
}

// HasToolRestrictions checks if an agent has tool restrictions
func (p *AgentProfile) HasToolRestrictions() bool {
	return len(p.AllowedTools) > 0
}

// IsToolAllowed checks if a tool is allowed for this agent
func (p *AgentProfile) IsToolAllowed(toolName string) bool {
	if !p.HasToolRestrictions() {
		return true // No restrictions = all tools allowed
	}

	for _, allowed := range p.AllowedTools {
		if allowed == toolName {
			return true
		}
	}
	return false
}

// resolveCapabilityPolicy converts config-level capability policy (with *bool
// defaults) into a concrete runtime policy. Returns nil when no policy is
// configured (backward compatible).
func resolveCapabilityPolicy(cfg *config.CapabilityPolicyConfig) *tools.CapabilityPolicy {
	if cfg == nil {
		return nil
	}

	p := &tools.CapabilityPolicy{
		Shell:            boolDefault(cfg.Shell, true),
		Network:          boolDefault(cfg.Network, true),
		NetworkAllowlist: cfg.NetworkAllowlist,
		Cron:             boolDefault(cfg.Cron, true),
		MemoryWrite:      boolDefault(cfg.MemoryWrite, true),
		Spawn:            boolDefault(cfg.Spawn, true),
		FilesystemRoots:  cfg.FilesystemRoots,
		FileReadOnly:     cfg.FileWriteScope == "read_only",
	}
	return p
}

func boolDefault(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}
