package rolejob

import (
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/config"
	"ok-gobot/internal/role"
	jobruntime "ok-gobot/internal/runtime"
)

func TestSelectorFromConfigEmptyIsNil(t *testing.T) {
	t.Parallel()

	selector, err := SelectorFromConfig(config.RuntimeConfig{})
	if err != nil {
		t.Fatalf("SelectorFromConfig error = %v", err)
	}
	if selector != nil {
		t.Fatal("empty config must yield a nil selector (feature off)")
	}
}

func TestSelectorFromConfigResolvesTiers(t *testing.T) {
	t.Parallel()

	selector, err := SelectorFromConfig(config.RuntimeConfig{
		CostTiers: map[string]config.CostTierEntry{
			"premium": {Model: "gpt-5.6-sol", Thinking: "high", MaxToolCalls: 75, MaxDuration: "20m"},
			"cheap":   {Model: "gpt-5.6-luna", Thinking: "low"},
		},
		Roles: []config.RolePolicyEntry{
			{Name: "monitor", DefaultTier: "cheap", Tiers: map[string]config.CostTierEntry{
				"cheap": {MaxToolCalls: 10},
			}},
		},
	})
	if err != nil {
		t.Fatalf("SelectorFromConfig error = %v", err)
	}
	if selector == nil {
		t.Fatal("expected a selector")
	}

	tier, cfg, err := selector.Resolve("", jobruntime.CostTierPremium)
	if err != nil {
		t.Fatalf("Resolve premium: %v", err)
	}
	if tier != jobruntime.CostTierPremium || cfg.Model != "gpt-5.6-sol" || cfg.MaxDuration != 20*time.Minute {
		t.Fatalf("premium = %s/%+v, want parsed global tier", tier, cfg)
	}

	// Role overlay: monitor's cheap tier caps tool calls but inherits the
	// global cheap model.
	tier, cfg, err = selector.ResolveForRole("monitor")
	if err != nil {
		t.Fatalf("ResolveForRole monitor: %v", err)
	}
	if tier != jobruntime.CostTierCheap || cfg.Model != "gpt-5.6-luna" || cfg.MaxToolCalls != 10 {
		t.Fatalf("monitor = %s/%+v, want cheap overlay on global", tier, cfg)
	}
}

func TestSelectorFromConfigRejectsBadEntries(t *testing.T) {
	t.Parallel()

	if _, err := SelectorFromConfig(config.RuntimeConfig{
		CostTiers: map[string]config.CostTierEntry{"turbo": {Model: "x"}},
	}); err == nil || !strings.Contains(err.Error(), "unknown cost tier") {
		t.Fatalf("error = %v, want unknown tier", err)
	}

	if _, err := SelectorFromConfig(config.RuntimeConfig{
		CostTiers: map[string]config.CostTierEntry{"cheap": {MaxDuration: "not-a-duration"}},
	}); err == nil || !strings.Contains(err.Error(), "max_duration") {
		t.Fatalf("error = %v, want max_duration parse error", err)
	}
}

func TestResolveTierIsStrictlyOptIn(t *testing.T) {
	t.Parallel()

	selector, err := SelectorFromConfig(config.RuntimeConfig{
		CostTiers: map[string]config.CostTierEntry{
			"standard": {Model: "gpt-5.6-sol"},
			"premium":  {Model: "gpt-5.6-sol", Thinking: "high"},
		},
		Roles: []config.RolePolicyEntry{
			{Name: "researcher", DefaultTier: "premium", Tiers: map[string]config.CostTierEntry{
				"premium": {MaxToolCalls: 75},
			}},
		},
	})
	if err != nil {
		t.Fatalf("SelectorFromConfig: %v", err)
	}

	// Legacy manifest: no worker, no role policy — stays untiered even
	// though global tiers exist.
	if _, _, ok := ResolveTier(&role.Manifest{Name: "legacy"}, Options{Selector: selector}); ok {
		t.Fatal("legacy manifest without worker/policy must stay untiered")
	}

	// Role policy without worker: default_tier is honored.
	tier, cfg, ok := ResolveTier(&role.Manifest{Name: "researcher"}, Options{Selector: selector})
	if !ok {
		t.Fatal("role with a policy must resolve its default tier")
	}
	if tier != jobruntime.CostTierPremium || cfg.MaxToolCalls != 75 || cfg.Model != "gpt-5.6-sol" {
		t.Fatalf("researcher default = %s/%+v, want premium overlay", tier, cfg)
	}

	// Explicit worker resolves through the role policy: researcher only
	// configures premium, so a standard request falls back to it (the
	// role's configured tier set bounds the choice).
	tier, _, ok = ResolveTier(&role.Manifest{Name: "researcher", Worker: "standard"}, Options{Selector: selector})
	if !ok || tier != jobruntime.CostTierPremium {
		t.Fatalf("explicit worker via role policy = %s (ok=%v), want premium fallback", tier, ok)
	}

	// Explicit worker on a role without a policy resolves against globals.
	tier, _, ok = ResolveTier(&role.Manifest{Name: "no-policy", Worker: "standard"}, Options{Selector: selector})
	if !ok || tier != jobruntime.CostTierStandard {
		t.Fatalf("explicit worker via globals = %s (ok=%v), want standard", tier, ok)
	}

	// Unknown worker tier: untiered, not coerced.
	if _, _, ok := ResolveTier(&role.Manifest{Name: "x", Worker: "turbo"}, Options{Selector: selector}); ok {
		t.Fatal("unknown worker tier must run untiered")
	}
}

func TestJobSpecTimeoutFallsBackToTierBudget(t *testing.T) {
	t.Parallel()

	selector, err := SelectorFromConfig(config.RuntimeConfig{
		CostTiers: map[string]config.CostTierEntry{
			"cheap": {Model: "gpt-5.6-luna", MaxDuration: "7m"},
		},
	})
	if err != nil {
		t.Fatalf("SelectorFromConfig: %v", err)
	}

	m := &role.Manifest{Name: "monitor", Prompt: "p", Worker: "cheap"}
	spec, err := JobSpec(m, Options{Selector: selector})
	if err != nil {
		t.Fatalf("JobSpec: %v", err)
	}
	if spec.Timeout != 7*time.Minute {
		t.Fatalf("Timeout = %s, want the tier budget 7m", spec.Timeout)
	}

	// Manifest max_duration still wins over the tier.
	m2 := &role.Manifest{Name: "monitor", Prompt: "p", Worker: "cheap", MaxDuration: 3 * time.Minute}
	spec2, err := JobSpec(m2, Options{Selector: selector})
	if err != nil {
		t.Fatalf("JobSpec: %v", err)
	}
	if spec2.Timeout != 3*time.Minute {
		t.Fatalf("Timeout = %s, manifest budget must win", spec2.Timeout)
	}
}

func TestJobSpecTimeoutPrecedenceWithFallback(t *testing.T) {
	t.Parallel()

	selector, err := SelectorFromConfig(config.RuntimeConfig{
		CostTiers: map[string]config.CostTierEntry{
			"cheap": {Model: "gpt-5.6-luna", MaxDuration: "7m"},
		},
	})
	if err != nil {
		t.Fatalf("SelectorFromConfig: %v", err)
	}

	// Tier beats the caller fallback.
	m := &role.Manifest{Name: "monitor", Prompt: "p", Worker: "cheap"}
	spec, err := JobSpec(m, Options{Selector: selector, FallbackTimeout: 15 * time.Minute})
	if err != nil {
		t.Fatalf("JobSpec: %v", err)
	}
	if spec.Timeout != 7*time.Minute {
		t.Fatalf("Timeout = %s, tier must beat the fallback", spec.Timeout)
	}

	// Untiered role: fallback beats DefaultTimeout.
	m2 := &role.Manifest{Name: "legacy", Prompt: "p"}
	spec2, err := JobSpec(m2, Options{Selector: selector, FallbackTimeout: 15 * time.Minute})
	if err != nil {
		t.Fatalf("JobSpec: %v", err)
	}
	if spec2.Timeout != 15*time.Minute {
		t.Fatalf("Timeout = %s, caller fallback must apply for untiered roles", spec2.Timeout)
	}
}
