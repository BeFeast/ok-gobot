package deepthink

import (
	"fmt"
	"sort"
	"strings"
)

// Target is the execution setting an escalation resolves to.
type Target struct {
	Tier     string // cost tier name when the target came from a tier; empty for an explicit model
	Model    string // alias-resolved model id
	Thinking string // thinking level; empty keeps the run default
}

// Policy decides which tiers and models the deep_think tool may escalate to.
// Everything comes from configuration; the model's own arguments are checked
// against it, never trusted.
type Policy struct {
	tierOrder       []string
	tiers           map[string]Target
	models          map[string]struct{}
	onRequest       map[string]struct{}
	aliases         map[string]string
	modelThinking   string
	onRequestConfig []string
	modelsConfig    []string
}

// Request is what the tool asks the policy to resolve. UserMessage is the
// text of the current user turn; it is the only evidence accepted for
// "the user explicitly asked for this model".
type Request struct {
	Tier        string
	Model       string
	UserMessage string
}

// PolicyOptions configures NewPolicy.
type PolicyOptions struct {
	// Tiers maps allowed tier names to their targets, in the order they were
	// configured. The first entry is the default when the tool names nothing.
	Tiers []TierTarget
	// Models may be requested without the user naming them.
	Models []string
	// OnRequestModels may be requested only when the current user message
	// names the model (or one of its aliases).
	OnRequestModels []string
	// Aliases resolve short names to model ids (config.model_aliases).
	Aliases map[string]string
	// ModelThinking is the thinking level applied when an explicit model is
	// chosen. Empty keeps the run default.
	ModelThinking string
}

// TierTarget pairs a tier name with its resolved settings.
type TierTarget struct {
	Name     string
	Model    string
	Thinking string
}

// NewPolicy builds the escalation allowlist. It returns nil when nothing is
// allowed, which callers treat as "tool disabled".
func NewPolicy(opts PolicyOptions) *Policy {
	p := &Policy{
		tiers:         map[string]Target{},
		models:        map[string]struct{}{},
		onRequest:     map[string]struct{}{},
		aliases:       map[string]string{},
		modelThinking: strings.TrimSpace(opts.ModelThinking),
	}
	for k, v := range opts.Aliases {
		p.aliases[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	for _, t := range opts.Tiers {
		name := strings.ToLower(strings.TrimSpace(t.Name))
		model := strings.TrimSpace(t.Model)
		if name == "" || model == "" {
			continue
		}
		if _, dup := p.tiers[name]; dup {
			continue
		}
		p.tierOrder = append(p.tierOrder, name)
		p.tiers[name] = Target{Tier: name, Model: p.resolveAlias(model), Thinking: strings.TrimSpace(t.Thinking)}
	}
	for _, m := range opts.Models {
		if resolved := p.resolveAlias(m); resolved != "" {
			p.models[resolved] = struct{}{}
			p.modelsConfig = append(p.modelsConfig, resolved)
		}
	}
	for _, m := range opts.OnRequestModels {
		if resolved := p.resolveAlias(m); resolved != "" {
			p.onRequest[resolved] = struct{}{}
			p.onRequestConfig = append(p.onRequestConfig, resolved)
		}
	}
	if len(p.tiers) == 0 && len(p.models) == 0 && len(p.onRequest) == 0 {
		return nil
	}
	return p
}

// Tiers returns the allowed tier names in configuration order.
func (p *Policy) Tiers() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.tierOrder...)
}

// Models returns models allowed without an explicit user request.
func (p *Policy) Models() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.modelsConfig...)
}

// OnRequestModels returns models allowed only when the user names them.
func (p *Policy) OnRequestModels() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.onRequestConfig...)
}

// Resolve applies the allowlist to a tool request.
func (p *Policy) Resolve(req Request) (Target, error) {
	if p == nil {
		return Target{}, fmt.Errorf("deep_think is not configured")
	}
	tier := strings.ToLower(strings.TrimSpace(req.Tier))
	model := strings.TrimSpace(req.Model)
	switch {
	case tier != "" && model != "":
		return Target{}, fmt.Errorf("pass either tier or model, not both")
	case tier != "":
		target, ok := p.tiers[tier]
		if !ok {
			return Target{}, fmt.Errorf("tier %q is not allowed for deep_think; allowed tiers: %s", tier, p.describeTiers())
		}
		return target, nil
	case model != "":
		resolved := p.resolveAlias(model)
		if _, ok := p.models[resolved]; ok {
			return Target{Model: resolved, Thinking: p.modelThinking}, nil
		}
		if _, ok := p.onRequest[resolved]; ok {
			if !p.userNamedModel(req.UserMessage, model, resolved) {
				return Target{}, fmt.Errorf("model %q may be used only when the user explicitly asks for it, and the current message does not name it; use a tier instead (%s)", resolved, p.describeAllowed())
			}
			return Target{Model: resolved, Thinking: p.modelThinking}, nil
		}
		return Target{}, fmt.Errorf("model %q is not allowed for deep_think; %s", model, p.describeAllowed())
	default:
		if len(p.tierOrder) > 0 {
			return p.tiers[p.tierOrder[0]], nil
		}
		if len(p.modelsConfig) > 0 {
			return Target{Model: p.modelsConfig[0], Thinking: p.modelThinking}, nil
		}
		return Target{}, fmt.Errorf("deep_think has no default target; %s", p.describeAllowed())
	}
}

// userNamedModel reports whether the user's own message names the model:
// the id itself, the name the tool passed, or any alias resolving to it.
func (p *Policy) userNamedModel(userMessage, requested, resolved string) bool {
	haystack := strings.ToLower(userMessage)
	if strings.TrimSpace(haystack) == "" {
		return false
	}
	candidates := []string{strings.ToLower(requested), strings.ToLower(resolved)}
	for alias, target := range p.aliases {
		if target == resolved {
			candidates = append(candidates, alias)
		}
	}
	for _, c := range candidates {
		if c != "" && strings.Contains(haystack, c) {
			return true
		}
	}
	return false
}

func (p *Policy) resolveAlias(name string) string {
	name = strings.TrimSpace(name)
	if resolved, ok := p.aliases[strings.ToLower(name)]; ok && resolved != "" {
		return resolved
	}
	return name
}

func (p *Policy) describeTiers() string {
	if len(p.tierOrder) == 0 {
		return "none"
	}
	return strings.Join(p.tierOrder, ", ")
}

func (p *Policy) describeAllowed() string {
	parts := []string{"allowed tiers: " + p.describeTiers()}
	if len(p.modelsConfig) > 0 {
		models := append([]string(nil), p.modelsConfig...)
		sort.Strings(models)
		parts = append(parts, "models: "+strings.Join(models, ", "))
	}
	if len(p.onRequestConfig) > 0 {
		models := append([]string(nil), p.onRequestConfig...)
		sort.Strings(models)
		parts = append(parts, "models only on explicit user request: "+strings.Join(models, ", "))
	}
	return strings.Join(parts, "; ")
}

// Describe renders the allowlist for the tool description.
func (p *Policy) Describe() string {
	if p == nil {
		return "deep_think is not configured"
	}
	return p.describeAllowed()
}
