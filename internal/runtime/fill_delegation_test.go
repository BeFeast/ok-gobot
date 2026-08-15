package runtime

import (
	"testing"
	"time"

	"ok-gobot/internal/delegation"
)

func TestFillDelegationFillsOnlyZeroFields(t *testing.T) {
	t.Parallel()

	tier := TierConfig{Model: "tier-model", Thinking: "low", MaxToolCalls: 30, MaxDuration: 5 * time.Minute}

	filled := FillDelegation(delegation.Job{}, tier)
	if filled.Model != "tier-model" || filled.Thinking != "low" || filled.MaxToolCalls != 30 || filled.MaxDuration != 5*time.Minute {
		t.Fatalf("empty base not filled from tier: %+v", filled)
	}

	explicit := delegation.Job{Model: "user-model", Thinking: "high", MaxToolCalls: 75, MaxDuration: 20 * time.Minute}
	kept := FillDelegation(explicit, tier)
	if kept.Model != explicit.Model || kept.Thinking != explicit.Thinking || kept.MaxToolCalls != explicit.MaxToolCalls || kept.MaxDuration != explicit.MaxDuration {
		t.Fatalf("explicit base fields must win over the tier: %+v", kept)
	}

	partial := FillDelegation(delegation.Job{Model: "user-model"}, tier)
	if partial.Model != "user-model" {
		t.Fatalf("explicit model overwritten: %+v", partial)
	}
	if partial.Thinking != "low" || partial.MaxToolCalls != 30 {
		t.Fatalf("unset fields not filled: %+v", partial)
	}
}

func TestFillDelegationZeroTierIsNoOp(t *testing.T) {
	t.Parallel()

	base := delegation.Job{Model: "m", MaxToolCalls: 10}
	got := FillDelegation(base, TierConfig{})
	if got.Model != base.Model || got.Thinking != base.Thinking || got.MaxToolCalls != base.MaxToolCalls || got.MaxDuration != base.MaxDuration {
		t.Fatalf("zero tier must not change the job: %+v", got)
	}
}

func TestLastResortNeverPicksLocalUnlessRequested(t *testing.T) {
	t.Parallel()

	ws := NewWorkerSelector(map[CostTier]TierConfig{
		CostTierLocal: {Model: "local-model"},
	}, nil)

	if _, _, err := ws.Resolve("", CostTierPremium); err == nil {
		t.Fatal("premium request must not silently fall back to the local tier")
	}
	tier, cfg, err := ws.Resolve("", CostTierLocal)
	if err != nil || tier != CostTierLocal || cfg.Model != "local-model" {
		t.Fatalf("explicit local request = %s/%+v/%v, want local", tier, cfg, err)
	}
}
