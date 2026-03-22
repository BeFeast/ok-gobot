package runtime

import (
	"testing"
	"time"

	"ok-gobot/internal/delegation"
)

// ── ParseCostTier ───────────────────────────────────────────────────────────

func TestParseCostTier(t *testing.T) {
	tests := []struct {
		input string
		want  CostTier
		ok    bool
	}{
		{"premium", CostTierPremium, true},
		{"PREMIUM", CostTierPremium, true},
		{" Premium ", CostTierPremium, true},
		{"standard", CostTierStandard, true},
		{"", CostTierStandard, true},
		{"cheap", CostTierCheap, true},
		{"background", CostTierCheap, true},
		{"BACKGROUND", CostTierCheap, true},
		{"local", CostTierLocal, true},
		{"unknown", "", false},
		{"enterprise", "", false},
	}
	for _, tt := range tests {
		got, ok := ParseCostTier(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("ParseCostTier(%q) = (%q, %v), want (%q, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

// ── RolePolicy.Resolve ──────────────────────────────────────────────────────

func TestRolePolicyResolveDirectHit(t *testing.T) {
	rp := &RolePolicy{
		Name: "researcher",
		Tiers: map[CostTier]TierConfig{
			CostTierStandard: {Model: "sonnet"},
			CostTierCheap:    {Model: "haiku"},
		},
	}

	tier, tc, err := rp.Resolve(CostTierStandard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierStandard || tc.Model != "sonnet" {
		t.Errorf("got tier=%q model=%q, want standard/sonnet", tier, tc.Model)
	}
}

func TestRolePolicyResolveFallback(t *testing.T) {
	rp := &RolePolicy{
		Name: "monitor",
		Tiers: map[CostTier]TierConfig{
			CostTierCheap: {Model: "haiku"},
		},
	}

	// Request premium → falls back to standard → falls back to cheap.
	tier, tc, err := rp.Resolve(CostTierPremium)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierCheap || tc.Model != "haiku" {
		t.Errorf("got tier=%q model=%q, want cheap/haiku", tier, tc.Model)
	}
}

func TestRolePolicyResolveLocalFallback(t *testing.T) {
	rp := &RolePolicy{
		Name: "triage",
		Tiers: map[CostTier]TierConfig{
			CostTierStandard: {Model: "sonnet"},
		},
	}

	// Request local → falls to cheap (missing) → falls to standard.
	tier, tc, err := rp.Resolve(CostTierLocal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierStandard || tc.Model != "sonnet" {
		t.Errorf("got tier=%q model=%q, want standard/sonnet", tier, tc.Model)
	}
}

func TestRolePolicyResolveNilPolicy(t *testing.T) {
	var rp *RolePolicy
	_, _, err := rp.Resolve(CostTierStandard)
	if err == nil {
		t.Error("expected error for nil policy, got nil")
	}
}

func TestRolePolicyResolveEmptyTiers(t *testing.T) {
	rp := &RolePolicy{Name: "empty", Tiers: map[CostTier]TierConfig{}}
	_, _, err := rp.Resolve(CostTierStandard)
	if err == nil {
		t.Error("expected error for empty tiers, got nil")
	}
}

// ── RolePolicy.ResolveDefault ───────────────────────────────────────────────

func TestRolePolicyResolveDefault(t *testing.T) {
	rp := &RolePolicy{
		Name:        "builder",
		DefaultTier: CostTierCheap,
		Tiers: map[CostTier]TierConfig{
			CostTierCheap:    {Model: "haiku"},
			CostTierStandard: {Model: "sonnet"},
		},
	}

	tier, tc, err := rp.ResolveDefault()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierCheap || tc.Model != "haiku" {
		t.Errorf("got tier=%q model=%q, want cheap/haiku", tier, tc.Model)
	}
}

func TestRolePolicyResolveDefaultFallsToStandard(t *testing.T) {
	rp := &RolePolicy{
		Name: "default-test",
		Tiers: map[CostTier]TierConfig{
			CostTierStandard: {Model: "sonnet"},
		},
	}

	tier, _, err := rp.ResolveDefault()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierStandard {
		t.Errorf("got tier=%q, want standard", tier)
	}
}

func TestRolePolicyResolveDefaultNilPolicy(t *testing.T) {
	var rp *RolePolicy
	_, _, err := rp.ResolveDefault()
	if err == nil {
		t.Error("expected error for nil policy, got nil")
	}
}

// ── RolePolicy.HasTier / AvailableTiers ─────────────────────────────────────

func TestRolePolicyHasTier(t *testing.T) {
	rp := &RolePolicy{
		Name: "test",
		Tiers: map[CostTier]TierConfig{
			CostTierStandard: {Model: "sonnet"},
			CostTierLocal:    {Model: "llama3"},
		},
	}

	if !rp.HasTier(CostTierStandard) {
		t.Error("expected HasTier(standard) = true")
	}
	if !rp.HasTier(CostTierLocal) {
		t.Error("expected HasTier(local) = true")
	}
	if rp.HasTier(CostTierPremium) {
		t.Error("expected HasTier(premium) = false")
	}
}

func TestRolePolicyHasTierNil(t *testing.T) {
	var rp *RolePolicy
	if rp.HasTier(CostTierStandard) {
		t.Error("expected HasTier on nil policy = false")
	}
}

func TestRolePolicyAvailableTiers(t *testing.T) {
	rp := &RolePolicy{
		Name: "test",
		Tiers: map[CostTier]TierConfig{
			CostTierPremium: {Model: "opus"},
			CostTierCheap:   {Model: "haiku"},
			CostTierLocal:   {Model: "llama3"},
		},
	}

	got := rp.AvailableTiers()
	want := []CostTier{CostTierCheap, CostTierPremium, CostTierLocal}
	if len(got) != len(want) {
		t.Fatalf("AvailableTiers() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("AvailableTiers()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestRolePolicyAvailableTiersNil(t *testing.T) {
	var rp *RolePolicy
	if got := rp.AvailableTiers(); got != nil {
		t.Errorf("AvailableTiers on nil policy = %v, want nil", got)
	}
}

// ── MergeDelegation ─────────────────────────────────────────────────────────

func TestMergeDelegation(t *testing.T) {
	base := delegation.Job{
		Model:        "sonnet",
		Thinking:     "medium",
		MaxToolCalls: 50,
		MaxDuration:  10 * time.Minute,
		OutputFormat: "markdown",
	}

	tier := TierConfig{
		Model:    "opus",
		Thinking: "high",
	}

	got := MergeDelegation(base, tier)
	if got.Model != "opus" {
		t.Errorf("Model = %q, want opus", got.Model)
	}
	if got.Thinking != "high" {
		t.Errorf("Thinking = %q, want high", got.Thinking)
	}
	if got.MaxToolCalls != 50 {
		t.Errorf("MaxToolCalls = %d, want 50 (preserved from base)", got.MaxToolCalls)
	}
	if got.MaxDuration != 10*time.Minute {
		t.Errorf("MaxDuration = %v, want 10m (preserved from base)", got.MaxDuration)
	}
	if got.OutputFormat != "markdown" {
		t.Errorf("OutputFormat = %q, want markdown (preserved from base)", got.OutputFormat)
	}
}

func TestMergeDelegationEmptyTier(t *testing.T) {
	base := delegation.Job{Model: "sonnet", Thinking: "low"}
	got := MergeDelegation(base, TierConfig{})
	if got.Model != "sonnet" || got.Thinking != "low" {
		t.Errorf("empty tier should preserve base, got Model=%q Thinking=%q", got.Model, got.Thinking)
	}
}

func TestMergeDelegationOverridesBudget(t *testing.T) {
	base := delegation.Job{MaxToolCalls: 50, MaxDuration: 10 * time.Minute}
	tier := TierConfig{MaxToolCalls: 20, MaxDuration: 3 * time.Minute}

	got := MergeDelegation(base, tier)
	if got.MaxToolCalls != 20 {
		t.Errorf("MaxToolCalls = %d, want 20", got.MaxToolCalls)
	}
	if got.MaxDuration != 3*time.Minute {
		t.Errorf("MaxDuration = %v, want 3m", got.MaxDuration)
	}
}

// ── TierConfig.DelegationOverrides ──────────────────────────────────────────

func TestTierConfigDelegationOverrides(t *testing.T) {
	tc := TierConfig{
		Model:        "opus",
		Provider:     "anthropic",
		Thinking:     "high",
		MaxToolCalls: 100,
		MaxDuration:  30 * time.Minute,
	}

	job := tc.DelegationOverrides()
	if job.Model != "opus" {
		t.Errorf("Model = %q, want opus", job.Model)
	}
	if job.Thinking != "high" {
		t.Errorf("Thinking = %q, want high", job.Thinking)
	}
	if job.MaxToolCalls != 100 {
		t.Errorf("MaxToolCalls = %d, want 100", job.MaxToolCalls)
	}
	if job.MaxDuration != 30*time.Minute {
		t.Errorf("MaxDuration = %v, want 30m", job.MaxDuration)
	}
}

// ── WorkerSelector ──────────────────────────────────────────────────────────

func newTestSelector() *WorkerSelector {
	globals := map[CostTier]TierConfig{
		CostTierPremium:  {Model: "opus", Thinking: "high", MaxToolCalls: 100},
		CostTierStandard: {Model: "sonnet", Thinking: "medium", MaxToolCalls: 50},
		CostTierCheap:    {Model: "haiku", Thinking: "off", MaxToolCalls: 20},
	}

	roles := []*RolePolicy{
		{
			Name:        "researcher",
			DefaultTier: CostTierStandard,
			Tiers: map[CostTier]TierConfig{
				CostTierPremium:  {Thinking: "high"},
				CostTierStandard: {Model: "sonnet-research", Thinking: "medium"},
			},
		},
		{
			Name:        "monitor",
			DefaultTier: CostTierCheap,
			Tiers: map[CostTier]TierConfig{
				CostTierCheap: {MaxToolCalls: 10, MaxDuration: 2 * time.Minute},
			},
		},
	}

	return NewWorkerSelector(globals, roles)
}

func TestWorkerSelectorResolveRoleDirect(t *testing.T) {
	ws := newTestSelector()

	tier, tc, err := ws.Resolve("researcher", CostTierStandard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierStandard {
		t.Errorf("tier = %q, want standard", tier)
	}
	// Role overrides model; global provides base MaxToolCalls.
	if tc.Model != "sonnet-research" {
		t.Errorf("Model = %q, want sonnet-research", tc.Model)
	}
	if tc.Thinking != "medium" {
		t.Errorf("Thinking = %q, want medium", tc.Thinking)
	}
	if tc.MaxToolCalls != 50 {
		t.Errorf("MaxToolCalls = %d, want 50 (from global base)", tc.MaxToolCalls)
	}
}

func TestWorkerSelectorResolveRoleMergesGlobal(t *testing.T) {
	ws := newTestSelector()

	// Researcher's premium tier only overrides Thinking; Model comes from global.
	tier, tc, err := ws.Resolve("researcher", CostTierPremium)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierPremium {
		t.Errorf("tier = %q, want premium", tier)
	}
	if tc.Model != "opus" {
		t.Errorf("Model = %q, want opus (from global)", tc.Model)
	}
	if tc.Thinking != "high" {
		t.Errorf("Thinking = %q, want high", tc.Thinking)
	}
	if tc.MaxToolCalls != 100 {
		t.Errorf("MaxToolCalls = %d, want 100 (from global)", tc.MaxToolCalls)
	}
}

func TestWorkerSelectorResolveRoleFallback(t *testing.T) {
	ws := newTestSelector()

	// Monitor has no premium tier. Fallback: premium → standard → cheap.
	// Monitor has cheap configured.
	tier, tc, err := ws.Resolve("monitor", CostTierPremium)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierCheap {
		t.Errorf("tier = %q, want cheap (fallback)", tier)
	}
	// Merged: global cheap (haiku, off, 20) + role cheap (10 tool calls, 2m).
	if tc.Model != "haiku" {
		t.Errorf("Model = %q, want haiku", tc.Model)
	}
	if tc.MaxToolCalls != 10 {
		t.Errorf("MaxToolCalls = %d, want 10 (role override)", tc.MaxToolCalls)
	}
	if tc.MaxDuration != 2*time.Minute {
		t.Errorf("MaxDuration = %v, want 2m (role override)", tc.MaxDuration)
	}
}

func TestWorkerSelectorResolveUnknownRole(t *testing.T) {
	ws := newTestSelector()

	// Unknown role → global tiers.
	tier, tc, err := ws.Resolve("unknown", CostTierStandard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierStandard {
		t.Errorf("tier = %q, want standard", tier)
	}
	if tc.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet (from global)", tc.Model)
	}
}

func TestWorkerSelectorResolveEmptyTier(t *testing.T) {
	ws := newTestSelector()

	// Empty tier string defaults to standard.
	tier, tc, err := ws.Resolve("researcher", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierStandard {
		t.Errorf("tier = %q, want standard (default)", tier)
	}
	if tc.Model != "sonnet-research" {
		t.Errorf("Model = %q, want sonnet-research", tc.Model)
	}
}

func TestWorkerSelectorResolveForRole(t *testing.T) {
	ws := newTestSelector()

	tier, tc, err := ws.ResolveForRole("monitor")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierCheap {
		t.Errorf("tier = %q, want cheap (monitor default)", tier)
	}
	if tc.MaxToolCalls != 10 {
		t.Errorf("MaxToolCalls = %d, want 10", tc.MaxToolCalls)
	}
}

func TestWorkerSelectorResolveForRoleUnknown(t *testing.T) {
	ws := newTestSelector()

	tier, _, err := ws.ResolveForRole("unknown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierStandard {
		t.Errorf("tier = %q, want standard (global fallback)", tier)
	}
}

func TestWorkerSelectorRole(t *testing.T) {
	ws := newTestSelector()

	if rp := ws.Role("researcher"); rp == nil {
		t.Error("expected non-nil role for researcher")
	}
	if rp := ws.Role("nonexistent"); rp != nil {
		t.Error("expected nil role for nonexistent")
	}
}

func TestWorkerSelectorHasLocalTier(t *testing.T) {
	ws := newTestSelector()
	if ws.HasLocalTier() {
		t.Error("expected no local tier in test selector")
	}

	// Add a role with a local tier.
	wsWithLocal := NewWorkerSelector(nil, []*RolePolicy{
		{Name: "local-role", Tiers: map[CostTier]TierConfig{
			CostTierLocal: {Model: "llama3", BaseURL: "http://localhost:11434/v1"},
		}},
	})
	if !wsWithLocal.HasLocalTier() {
		t.Error("expected local tier to be detected")
	}

	// Global local tier.
	wsGlobalLocal := NewWorkerSelector(
		map[CostTier]TierConfig{CostTierLocal: {Model: "llama3"}},
		nil,
	)
	if !wsGlobalLocal.HasLocalTier() {
		t.Error("expected global local tier to be detected")
	}
}

func TestWorkerSelectorRegisteredRoles(t *testing.T) {
	ws := newTestSelector()
	names := ws.RegisteredRoles()
	if len(names) != 2 {
		t.Fatalf("RegisteredRoles() returned %d names, want 2", len(names))
	}

	found := make(map[string]bool)
	for _, n := range names {
		found[n] = true
	}
	if !found["researcher"] || !found["monitor"] {
		t.Errorf("RegisteredRoles() = %v, want [researcher, monitor]", names)
	}
}

func TestWorkerSelectorResolveFallbackGlobalBase(t *testing.T) {
	// Global only has standard; role defines cheap with overrides only.
	// Requesting premium on the role should fall back to cheap (role tier)
	// and merge on top of the nearest global tier (standard), not a zero config.
	ws := NewWorkerSelector(
		map[CostTier]TierConfig{
			CostTierStandard: {Model: "sonnet", Provider: "anthropic", Thinking: "medium", MaxToolCalls: 50},
		},
		[]*RolePolicy{
			{
				Name: "worker",
				Tiers: map[CostTier]TierConfig{
					CostTierCheap: {MaxToolCalls: 10},
				},
			},
		},
	)

	tier, tc, err := ws.Resolve("worker", CostTierPremium)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierCheap {
		t.Errorf("tier = %q, want cheap (role fallback)", tier)
	}
	if tc.MaxToolCalls != 10 {
		t.Errorf("MaxToolCalls = %d, want 10 (role override)", tc.MaxToolCalls)
	}
	// Model and Provider must come from the global fallback, not be empty.
	if tc.Model != "sonnet" {
		t.Errorf("Model = %q, want sonnet (from global fallback base)", tc.Model)
	}
	if tc.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic (from global fallback base)", tc.Provider)
	}
}

func TestWorkerSelectorNilInputs(t *testing.T) {
	ws := NewWorkerSelector(nil, nil)
	_, _, err := ws.Resolve("any", CostTierStandard)
	if err == nil {
		t.Error("expected error resolving with no tiers configured")
	}
}

func TestWorkerSelectorGlobalOnlyFallback(t *testing.T) {
	ws := NewWorkerSelector(
		map[CostTier]TierConfig{
			CostTierCheap: {Model: "haiku"},
		},
		nil,
	)

	// Request premium → standard (miss) → cheap (hit).
	tier, tc, err := ws.Resolve("", CostTierPremium)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tier != CostTierCheap {
		t.Errorf("tier = %q, want cheap", tier)
	}
	if tc.Model != "haiku" {
		t.Errorf("Model = %q, want haiku", tc.Model)
	}
}

// ── mergeTierConfig ─────────────────────────────────────────────────────────

func TestMergeTierConfig(t *testing.T) {
	base := TierConfig{
		Model:        "sonnet",
		Provider:     "anthropic",
		Thinking:     "medium",
		MaxToolCalls: 50,
		MaxDuration:  10 * time.Minute,
	}

	override := TierConfig{
		Model:    "opus",
		Thinking: "high",
	}

	got := mergeTierConfig(base, override)
	if got.Model != "opus" {
		t.Errorf("Model = %q, want opus", got.Model)
	}
	if got.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic (preserved)", got.Provider)
	}
	if got.Thinking != "high" {
		t.Errorf("Thinking = %q, want high", got.Thinking)
	}
	if got.MaxToolCalls != 50 {
		t.Errorf("MaxToolCalls = %d, want 50 (preserved)", got.MaxToolCalls)
	}
	if got.MaxDuration != 10*time.Minute {
		t.Errorf("MaxDuration = %v, want 10m (preserved)", got.MaxDuration)
	}
}

func TestMergeTierConfigBaseURL(t *testing.T) {
	base := TierConfig{Model: "haiku"}
	override := TierConfig{BaseURL: "http://localhost:11434/v1"}

	got := mergeTierConfig(base, override)
	if got.Model != "haiku" {
		t.Errorf("Model = %q, want haiku", got.Model)
	}
	if got.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("BaseURL = %q, want http://localhost:11434/v1", got.BaseURL)
	}
}
