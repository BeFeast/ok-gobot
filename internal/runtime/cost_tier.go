package runtime

import (
	"fmt"
	"strings"
	"time"

	"ok-gobot/internal/delegation"
)

// CostTier classifies work by expected resource expenditure.
type CostTier string

const (
	// CostTierPremium selects the most capable (and expensive) model.
	CostTierPremium CostTier = "premium"
	// CostTierStandard selects a balanced cost/quality model.
	CostTierStandard CostTier = "standard"
	// CostTierCheap selects a low-cost model suitable for background or bulk work.
	CostTierCheap CostTier = "cheap"
	// CostTierLocal selects an optional locally-hosted model (e.g. ollama).
	CostTierLocal CostTier = "local"
)

// costTierFallbackOrder defines which tiers to try when the requested tier
// is not configured. Resolution walks this list in order and stops at the
// first configured tier.
var costTierFallbackOrder = map[CostTier][]CostTier{
	CostTierPremium:  {CostTierStandard, CostTierCheap},
	CostTierStandard: {CostTierCheap, CostTierPremium},
	CostTierCheap:    {CostTierStandard},
	CostTierLocal:    {CostTierCheap, CostTierStandard},
}

// allTiersOrdered is used for last-resort scanning when the fallback chain
// is exhausted.
var allTiersOrdered = []CostTier{
	CostTierStandard, CostTierCheap, CostTierPremium, CostTierLocal,
}

// ParseCostTier normalizes s into a valid CostTier.
// "background" is accepted as an alias for CostTierCheap.
// An empty string maps to CostTierStandard.
func ParseCostTier(s string) (CostTier, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "premium":
		return CostTierPremium, true
	case "standard", "":
		return CostTierStandard, true
	case "cheap", "background":
		return CostTierCheap, true
	case "local":
		return CostTierLocal, true
	default:
		return "", false
	}
}

// TierConfig describes concrete execution settings for one cost tier.
type TierConfig struct {
	Model        string
	Provider     string
	BaseURL      string
	Thinking     string
	MaxToolCalls int
	MaxDuration  time.Duration
}

// DelegationOverrides produces a delegation.Job with the tier's non-zero
// fields set. Callers merge this with their base Job.
func (tc TierConfig) DelegationOverrides() delegation.Job {
	return delegation.Job{
		Model:        tc.Model,
		Thinking:     tc.Thinking,
		MaxToolCalls: tc.MaxToolCalls,
		MaxDuration:  tc.MaxDuration,
	}
}

// MergeDelegation applies non-zero tier overrides on top of a base
// delegation.Job and returns the result. The base is not modified.
func MergeDelegation(base delegation.Job, tier TierConfig) delegation.Job {
	if tier.Model != "" {
		base.Model = tier.Model
	}
	if tier.Thinking != "" {
		base.Thinking = tier.Thinking
	}
	if tier.MaxToolCalls > 0 {
		base.MaxToolCalls = tier.MaxToolCalls
	}
	if tier.MaxDuration > 0 {
		base.MaxDuration = tier.MaxDuration
	}
	return base
}

// ── RolePolicy ──────────────────────────────────────────────────────────────

// RolePolicy maps a named role to per-tier execution settings.
type RolePolicy struct {
	// Name is a human-readable identifier for this role (e.g. "researcher").
	Name string
	// DefaultTier is used when no explicit tier is requested.
	DefaultTier CostTier
	// Tiers maps each configured cost tier to its execution settings.
	Tiers map[CostTier]TierConfig
}

// Resolve returns the TierConfig for the requested tier, falling back
// through the fallback chain when the exact tier is not configured.
// Returns an error only when no usable tier is found at all.
func (rp *RolePolicy) Resolve(tier CostTier) (CostTier, TierConfig, error) {
	if rp == nil || len(rp.Tiers) == 0 {
		return "", TierConfig{}, fmt.Errorf("role policy has no configured tiers")
	}
	if tc, ok := rp.Tiers[tier]; ok {
		return tier, tc, nil
	}
	for _, fb := range costTierFallbackOrder[tier] {
		if tc, ok := rp.Tiers[fb]; ok {
			return fb, tc, nil
		}
	}
	// Last resort: return the first configured tier in canonical order.
	for _, t := range allTiersOrdered {
		if tc, ok := rp.Tiers[t]; ok {
			return t, tc, nil
		}
	}
	return "", TierConfig{}, fmt.Errorf("role %q: no usable tier found", rp.Name)
}

// ResolveDefault returns the TierConfig for the role's default tier.
func (rp *RolePolicy) ResolveDefault() (CostTier, TierConfig, error) {
	if rp == nil {
		return "", TierConfig{}, fmt.Errorf("role policy has no configured tiers")
	}
	tier := rp.DefaultTier
	if tier == "" {
		tier = CostTierStandard
	}
	return rp.Resolve(tier)
}

// HasTier reports whether the role has a specific tier configured.
func (rp *RolePolicy) HasTier(tier CostTier) bool {
	if rp == nil {
		return false
	}
	_, ok := rp.Tiers[tier]
	return ok
}

// AvailableTiers returns the configured tiers in canonical order.
func (rp *RolePolicy) AvailableTiers() []CostTier {
	if rp == nil {
		return nil
	}
	out := make([]CostTier, 0, len(rp.Tiers))
	for _, t := range allTiersOrdered {
		if _, ok := rp.Tiers[t]; ok {
			out = append(out, t)
		}
	}
	return out
}

// ── WorkerSelector ──────────────────────────────────────────────────────────

// WorkerSelector resolves cost tiers across a global defaults layer and a
// per-role policy layer. Role-specific tier configs are merged on top of
// global defaults so that roles only need to declare their overrides.
type WorkerSelector struct {
	globals map[CostTier]TierConfig
	roles   map[string]*RolePolicy
}

// NewWorkerSelector creates a WorkerSelector from global tier defaults and
// a list of role policies. Either argument may be nil.
func NewWorkerSelector(globals map[CostTier]TierConfig, roles []*RolePolicy) *WorkerSelector {
	ws := &WorkerSelector{
		globals: globals,
		roles:   make(map[string]*RolePolicy, len(roles)),
	}
	if ws.globals == nil {
		ws.globals = make(map[CostTier]TierConfig)
	}
	for _, r := range roles {
		if r != nil {
			ws.roles[r.Name] = r
		}
	}
	return ws
}

// Resolve returns the best TierConfig for the given role and requested tier.
//
// Resolution order:
//  1. If the role exists and has the tier (or a fallback), merge the role
//     tier config on top of the matching global tier config.
//  2. If no role matches, resolve directly from global tiers with fallback.
//  3. Error if no usable tier is found anywhere.
func (ws *WorkerSelector) Resolve(roleName string, tier CostTier) (CostTier, TierConfig, error) {
	if tier == "" {
		tier = CostTierStandard
	}

	rp, hasRole := ws.roles[roleName]
	if hasRole {
		resolvedTier, roleTc, err := rp.Resolve(tier)
		if err == nil {
			globalTc := ws.resolveGlobalBase(resolvedTier)
			return resolvedTier, mergeTierConfig(globalTc, roleTc), nil
		}
	}

	return ws.resolveGlobal(tier)
}

// ResolveForRole resolves the default tier for a named role.
// If the role is not registered, it falls back to the global standard tier.
func (ws *WorkerSelector) ResolveForRole(roleName string) (CostTier, TierConfig, error) {
	rp, ok := ws.roles[roleName]
	if !ok {
		return ws.resolveGlobal(CostTierStandard)
	}
	tier := rp.DefaultTier
	if tier == "" {
		tier = CostTierStandard
	}
	return ws.Resolve(roleName, tier)
}

// Role returns the RolePolicy for name, or nil if not registered.
func (ws *WorkerSelector) Role(name string) *RolePolicy {
	return ws.roles[name]
}

// HasLocalTier reports whether any registered role or the global tier set
// includes a local cost tier.
func (ws *WorkerSelector) HasLocalTier() bool {
	if _, ok := ws.globals[CostTierLocal]; ok {
		return true
	}
	for _, rp := range ws.roles {
		if rp.HasTier(CostTierLocal) {
			return true
		}
	}
	return false
}

// RegisteredRoles returns the names of all registered role policies.
func (ws *WorkerSelector) RegisteredRoles() []string {
	names := make([]string, 0, len(ws.roles))
	for name := range ws.roles {
		names = append(names, name)
	}
	return names
}

// resolveGlobalBase returns the best-matching global TierConfig for the
// given tier, walking the fallback chain. Used as a base layer when merging
// role overrides. Returns a zero TierConfig if no global tier matches.
func (ws *WorkerSelector) resolveGlobalBase(tier CostTier) TierConfig {
	if tc, ok := ws.globals[tier]; ok {
		return tc
	}
	for _, fb := range costTierFallbackOrder[tier] {
		if tc, ok := ws.globals[fb]; ok {
			return tc
		}
	}
	for _, t := range allTiersOrdered {
		if tc, ok := ws.globals[t]; ok {
			return tc
		}
	}
	return TierConfig{}
}

// resolveGlobal resolves a tier from the global tier set with fallback.
func (ws *WorkerSelector) resolveGlobal(tier CostTier) (CostTier, TierConfig, error) {
	if tc, ok := ws.globals[tier]; ok {
		return tier, tc, nil
	}
	for _, fb := range costTierFallbackOrder[tier] {
		if tc, ok := ws.globals[fb]; ok {
			return fb, tc, nil
		}
	}
	for _, t := range allTiersOrdered {
		if tc, ok := ws.globals[t]; ok {
			return t, tc, nil
		}
	}
	return "", TierConfig{}, fmt.Errorf("no usable cost tier found")
}

// mergeTierConfig applies non-zero fields from override on top of base.
func mergeTierConfig(base, override TierConfig) TierConfig {
	if override.Model != "" {
		base.Model = override.Model
	}
	if override.Provider != "" {
		base.Provider = override.Provider
	}
	if override.BaseURL != "" {
		base.BaseURL = override.BaseURL
	}
	if override.Thinking != "" {
		base.Thinking = override.Thinking
	}
	if override.MaxToolCalls > 0 {
		base.MaxToolCalls = override.MaxToolCalls
	}
	if override.MaxDuration > 0 {
		base.MaxDuration = override.MaxDuration
	}
	return base
}
