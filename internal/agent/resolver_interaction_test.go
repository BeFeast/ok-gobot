package agent

import (
	"context"
	"errors"
	"testing"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/tools"
)

// interactionStubStore implements SessionStore for fast-lane tests.
type interactionStubStore struct {
	modelOverride string
	thinkLevel    string
	failReads     bool
}

func (s *interactionStubStore) GetModelOverride(_ int64) (string, error) {
	if s.failReads {
		return "", errors.New("db down")
	}
	return s.modelOverride, nil
}

func (s *interactionStubStore) GetActiveAgent(_ int64) (string, error) {
	return "default", nil
}

func (s *interactionStubStore) GetSessionOption(_ int64, key string) (string, error) {
	if s.failReads {
		return "", errors.New("db down")
	}
	if key == "think_level" {
		return s.thinkLevel, nil
	}
	return "", nil
}

func newLaneResolver(store *interactionStubStore) *RunResolver {
	return &RunResolver{
		Store: store,
		AIConfig: AIResolverConfig{
			Model:               "gpt-5.6-sol",
			DefaultThinking:     "high",
			InteractionModel:    "gpt-5.6-luna",
			InteractionThinking: "low",
		},
	}
}

func defaultLaneProfile(r *RunResolver) *AgentProfile {
	return &AgentProfile{Name: "default", Model: r.AIConfig.Model}
}

func TestInteractionLaneAppliesForFlaggedRuns(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{})
	profile := defaultLaneProfile(r)
	lane := &RunOverrides{UseInteraction: true}

	if got := r.resolveModel(42, profile, lane); got != "gpt-5.6-luna" {
		t.Fatalf("resolveModel = %q, want fast-lane model", got)
	}
	if got := r.resolveThinkLevel(42, profile, lane); got != "low" {
		t.Fatalf("resolveThinkLevel = %q, want low", got)
	}
}

func TestInteractionLaneOffWithoutFlag(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{})
	profile := defaultLaneProfile(r)

	if got := r.resolveModel(42, profile, nil); got != "gpt-5.6-sol" {
		t.Fatalf("resolveModel = %q, want default model for unflagged runs", got)
	}
	if got := r.resolveThinkLevel(42, profile, nil); got != "high" {
		t.Fatalf("resolveThinkLevel = %q, want default thinking", got)
	}
}

func TestInteractionLaneSitsUnderSessionChoices(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{modelOverride: "claude-opus-4-5-20251101", thinkLevel: "medium"})
	profile := defaultLaneProfile(r)
	lane := &RunOverrides{UseInteraction: true}

	if got := r.resolveModel(42, profile, lane); got != "claude-opus-4-5-20251101" {
		t.Fatalf("resolveModel = %q, session /model must win over the lane", got)
	}
	if got := r.resolveThinkLevel(42, profile, lane); got != "medium" {
		t.Fatalf("resolveThinkLevel = %q, session /think must win over the lane", got)
	}
}

func TestInteractionLaneSitsUnderExplicitOverrides(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{})
	profile := defaultLaneProfile(r)
	explicit := &RunOverrides{Model: "explicit-model", ThinkLevel: "xhigh", UseInteraction: true}

	if got := r.resolveModel(42, profile, explicit); got != "explicit-model" {
		t.Fatalf("resolveModel = %q, explicit override must win", got)
	}
	if got := r.resolveThinkLevel(42, profile, explicit); got != "xhigh" {
		t.Fatalf("resolveThinkLevel = %q, explicit override must win", got)
	}
}

func TestInteractionLaneDisabledOnStoreErrors(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{failReads: true})
	profile := defaultLaneProfile(r)
	lane := &RunOverrides{UseInteraction: true}

	if got := r.resolveModel(42, profile, lane); got != "gpt-5.6-sol" {
		t.Fatalf("resolveModel = %q, lane must yield on store read errors", got)
	}
	if got := r.resolveThinkLevel(42, profile, lane); got != "high" {
		t.Fatalf("resolveThinkLevel = %q, lane must yield on store read errors", got)
	}
}

func TestInteractionLaneRespectsProfileModels(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{})
	custom := &AgentProfile{Name: "researcher", Model: "claude-opus-4-5-20251101"}
	lane := &RunOverrides{UseInteraction: true}

	if got := r.resolveModel(42, custom, lane); got != "claude-opus-4-5-20251101" {
		t.Fatalf("resolveModel = %q, specialized profile model must win over the lane", got)
	}
	if got := r.resolveThinkLevel(42, custom, lane); got != "high" {
		t.Fatalf("resolveThinkLevel = %q, lane thinking must not touch specialized profiles", got)
	}
}

func TestInteractionLaneResolvesAliases(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{})
	r.AIConfig.InteractionModel = "luna"
	r.AIConfig.ModelAliases = map[string]string{"luna": "gpt-5.6-luna"}
	profile := defaultLaneProfile(r)

	if got := r.resolveModel(42, profile, &RunOverrides{UseInteraction: true}); got != "gpt-5.6-luna" {
		t.Fatalf("resolveModel = %q, lane alias must resolve", got)
	}
}

func TestResolveDegradesLaneOnPreflightFailure(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{})
	r.DefaultPersonality = &Personality{}
	r.ToolRegistry = tools.NewRegistry()
	r.AIConfig.Provider = "custom"
	r.AIConfig.APIKey = "test-key"
	r.AIConfig.BaseURL = "http://127.0.0.1:0"
	var preflighted []string
	r.AIConfig.BackendPreflight = func(_ context.Context, model, _, _ string) (ai.BackendHealth, error) {
		preflighted = append(preflighted, model)
		if model == "gpt-5.6-luna" {
			return ai.BackendHealth{}, errors.New("model not found")
		}
		return ai.BackendHealth{}, nil
	}

	components, err := r.Resolve(42, &RunOverrides{UseInteraction: true}, nil)
	if err != nil {
		t.Fatalf("Resolve error = %v, want degrade to the default lane instead", err)
	}
	if components.Model != "gpt-5.6-sol" {
		t.Fatalf("Model = %q, want default model after lane degrade", components.Model)
	}
	if len(preflighted) != 2 || preflighted[0] != "gpt-5.6-luna" || preflighted[1] != "gpt-5.6-sol" {
		t.Fatalf("preflight sequence = %v, want [gpt-5.6-luna gpt-5.6-sol]", preflighted)
	}
}

func TestTierDefaultsSitUnderSessionChoices(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{modelOverride: "claude-opus-4-5-20251101", thinkLevel: "medium"})
	profile := defaultLaneProfile(r)
	tiered := &RunOverrides{TierModel: "gpt-5.6-sol", TierThinking: "high"}

	if got := r.resolveModel(42, profile, tiered); got != "claude-opus-4-5-20251101" {
		t.Fatalf("resolveModel = %q, session /model must win over the tier default", got)
	}
	if got := r.resolveThinkLevel(42, profile, tiered); got != "medium" {
		t.Fatalf("resolveThinkLevel = %q, session /think must win over the tier default", got)
	}
}

func TestTierDefaultsApplyWithoutSessionPins(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{})
	profile := defaultLaneProfile(r)
	tiered := &RunOverrides{TierModel: "tier-model", TierThinking: "low"}

	if got := r.resolveModel(42, profile, tiered); got != "tier-model" {
		t.Fatalf("resolveModel = %q, want the tier default", got)
	}
	if got := r.resolveThinkLevel(42, profile, tiered); got != "low" {
		t.Fatalf("resolveThinkLevel = %q, want the tier default", got)
	}

	explicit := &RunOverrides{Model: "explicit", ThinkLevel: "xhigh", TierModel: "tier-model", TierThinking: "low"}
	if got := r.resolveModel(42, profile, explicit); got != "explicit" {
		t.Fatalf("resolveModel = %q, explicit override must win over the tier", got)
	}
	if got := r.resolveThinkLevel(42, profile, explicit); got != "xhigh" {
		t.Fatalf("resolveThinkLevel = %q, explicit override must win over the tier", got)
	}
}

func TestTierDefaultsDisabledOnStoreErrors(t *testing.T) {
	t.Parallel()

	r := newLaneResolver(&interactionStubStore{failReads: true})
	profile := defaultLaneProfile(r)
	tiered := &RunOverrides{TierModel: "tier-model", TierThinking: "low"}

	if got := r.resolveModel(42, profile, tiered); got != "gpt-5.6-sol" {
		t.Fatalf("resolveModel = %q, tier default must yield on store read errors", got)
	}
	if got := r.resolveThinkLevel(42, profile, tiered); got != "high" {
		t.Fatalf("resolveThinkLevel = %q, tier default must yield on store read errors", got)
	}
}
