package agent

import (
	"context"
	"errors"
	"sync"
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/deepthink"
	"ok-gobot/internal/tools"
)

type deepThinkStore struct {
	modelOverride string
	thinkLevel    string
}

func (s *deepThinkStore) GetModelOverride(int64) (string, error) { return s.modelOverride, nil }
func (s *deepThinkStore) GetActiveAgent(int64) (string, error)   { return "default", nil }
func (s *deepThinkStore) GetSessionOption(_ int64, key string) (string, error) {
	if key == "think_level" {
		return s.thinkLevel, nil
	}
	return "", nil
}

type preflightCall struct{ model, tier, effort string }

func newDeepThinkResolver(store SessionStore, preflight func(ctx context.Context, model, tier, effort string) (ai.BackendHealth, error)) *RunResolver {
	return &RunResolver{
		Store:              store,
		DefaultPersonality: &Personality{Files: map[string]string{"SOUL.md": "test"}},
		AIConfig: AIResolverConfig{
			Provider:         "test",
			Model:            "gpt-6-luna",
			DefaultThinking:  "low",
			DefaultClient:    &stubClient{},
			ModelAliases:     map[string]string{},
			BackendPreflight: preflight,
		},
		ToolRegistry: tools.NewRegistry(),
	}
}

// A deep-think turn is a hard override: it beats session /model and /think
// and carries the tier label into the run metadata.
func TestResolveDeepThinkOverridesSessionChoices(t *testing.T) {
	var mu sync.Mutex
	var calls []preflightCall
	resolver := newDeepThinkResolver(&deepThinkStore{modelOverride: "pinned-model", thinkLevel: "off"},
		func(_ context.Context, model, tier, effort string) (ai.BackendHealth, error) {
			mu.Lock()
			calls = append(calls, preflightCall{model, tier, effort})
			mu.Unlock()
			return ai.BackendHealth{Status: ai.BackendHealthHealthy}, nil
		})

	components, err := resolver.Resolve(7, &RunOverrides{Model: "gpt-6-sol", ThinkLevel: "high", Tier: "premium", DeepThink: true}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if components.Model != "gpt-6-sol" || components.Effort != "high" || components.ModelTier != "premium" {
		t.Fatalf("components = %s/%s/%s, want gpt-6-sol/high/premium", components.Model, components.Effort, components.ModelTier)
	}
	if len(calls) != 1 || calls[0] != (preflightCall{"gpt-6-sol", "premium", "high"}) {
		t.Fatalf("preflight calls = %+v", calls)
	}
}

// When preflight itself falls back (e.g. to the next model in
// ai.fallback_models) the run keeps the fallback identity and thinking level;
// no degrade happens and the indicator will show the real model.
func TestResolveDeepThinkKeepsPreflightFallbackIdentity(t *testing.T) {
	resolver := newDeepThinkResolver(&deepThinkStore{},
		func(_ context.Context, model, tier, effort string) (ai.BackendHealth, error) {
			return ai.BackendHealth{
				Status:   ai.BackendHealthUnavailable,
				Identity: ai.BackendIdentity{Model: "gpt-5.6-luna", Tier: tier, Effort: effort},
				Fallback: ai.FallbackDecision{Action: "fallback", ToModel: "gpt-5.6-luna", Reason: "sol unavailable"},
			}, nil
		})

	components, err := resolver.Resolve(7, &RunOverrides{Model: "gpt-6-sol", ThinkLevel: "high", Tier: "premium", DeepThink: true}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if components.Model != "gpt-5.6-luna" || components.Effort != "high" || components.ModelTier != "premium" {
		t.Fatalf("components = %s/%s/%s, want gpt-5.6-luna/high/premium", components.Model, components.Effort, components.ModelTier)
	}
}

// A preflight error on the deep-think target degrades the turn to the default
// lane (best-effort, like the interaction lane) instead of failing the reply,
// and the tier label is recomputed so nothing downstream claims "premium".
func TestResolveDeepThinkDegradesOnPreflightError(t *testing.T) {
	var calls []preflightCall
	resolver := newDeepThinkResolver(&deepThinkStore{},
		func(_ context.Context, model, tier, effort string) (ai.BackendHealth, error) {
			calls = append(calls, preflightCall{model, tier, effort})
			if model == "gpt-6-sol" {
				return ai.BackendHealth{}, errors.New("model retired")
			}
			return ai.BackendHealth{Status: ai.BackendHealthHealthy}, nil
		})

	components, err := resolver.Resolve(7, &RunOverrides{Model: "gpt-6-sol", ThinkLevel: "high", Tier: "premium", DeepThink: true}, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if components.Model != "gpt-6-luna" || components.Effort != "low" || components.ModelTier != "agent" {
		t.Fatalf("components = %s/%s/%s, want gpt-6-luna/low/agent", components.Model, components.Effort, components.ModelTier)
	}
	if len(calls) != 2 || calls[1] != (preflightCall{"gpt-6-luna", "agent", "low"}) {
		t.Fatalf("preflight calls = %+v", calls)
	}
}

// Without the DeepThink flag an explicit override still fails hard (the /task
// contract is unchanged).
func TestResolveExplicitOverrideStillFailsWithoutDeepThink(t *testing.T) {
	resolver := newDeepThinkResolver(&deepThinkStore{},
		func(_ context.Context, model, tier, effort string) (ai.BackendHealth, error) {
			return ai.BackendHealth{}, errors.New("model retired")
		})
	if _, err := resolver.Resolve(7, &RunOverrides{Model: "gpt-6-sol"}, nil); err == nil {
		t.Fatal("expected preflight error to surface")
	}
}

// The main chat agent gets deep_think only when a policy is configured;
// sub-agents never do (an escalation cannot escalate).
func TestBuildToolRegistry_DeepThinkIsMainAgentOnlyAndOptIn(t *testing.T) {
	t.Parallel()

	base := tools.NewRegistry()
	profile := &AgentProfile{}

	off := &RunResolver{ToolRegistry: base, MediaSender: &resolverMediaSender{}, SubagentSubmitter: stubSubagentSubmitter{}}
	if execLaneHasTool(off.buildToolRegistry(123, profile, false, nil), "deep_think") {
		t.Error("deep_think registered without a policy (feature must default off)")
	}

	on := &RunResolver{
		ToolRegistry:      base,
		MediaSender:       &resolverMediaSender{},
		SubagentSubmitter: stubSubagentSubmitter{},
		DeepThink: deepthink.NewPolicy(deepthink.PolicyOptions{
			Tiers: []deepthink.TierTarget{{Name: "premium", Model: "gpt-6-sol", Thinking: "high"}},
		}),
	}
	if !execLaneHasTool(on.buildToolRegistry(123, profile, false, nil), "deep_think") {
		t.Error("main agent must have deep_think when the policy is configured")
	}
	if execLaneHasTool(on.buildToolRegistry(123, profile, true, nil), "deep_think") {
		t.Error("sub-agent must not have deep_think (no recursive escalation)")
	}
	if execLaneHasTool(on.buildToolRegistry(0, profile, false, nil), "deep_think") {
		t.Error("deep_think must stay chat-bound (chatID 0 gets none)")
	}
}
