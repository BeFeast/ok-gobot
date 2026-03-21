package runtime

import (
	"fmt"
	"sync"
)

// CostTier classifies workers by operational cost.
type CostTier string

const (
	// TierPremium routes to high-quality, high-cost workers (e.g. Opus, GPT-4).
	TierPremium CostTier = "premium"
	// TierCheap routes to budget workers for background or batch work (e.g. Haiku, Flash).
	TierCheap CostTier = "cheap"
	// TierLocal routes to optional locally-hosted workers (e.g. Ollama).
	TierLocal CostTier = "local"
)

// ValidCostTiers is the set of recognised tier values.
var ValidCostTiers = map[CostTier]bool{
	TierPremium: true,
	TierCheap:   true,
	TierLocal:   true,
}

// RolePolicy describes which cost tiers a named role may use and in what
// order they should be tried. The first healthy worker matching the earliest
// tier wins.
type RolePolicy struct {
	// Name is a human-readable identifier for this role (e.g. "interactive", "background").
	Name string
	// Tiers lists the cost tiers in preference order. The router tries the
	// first tier, then falls back through subsequent tiers.
	Tiers []CostTier
	// MaxConcurrent limits parallel runs for this role. 0 means unlimited.
	MaxConcurrent int
}

// WorkerDef describes a registered worker endpoint tagged with a cost tier.
type WorkerDef struct {
	// Name uniquely identifies this worker.
	Name string
	// Tier is the cost classification of the worker.
	Tier CostTier
	// Healthy reports whether the worker is currently available.
	Healthy bool
}

// RoleRouter selects workers based on role policy tier preferences.
//
// Callers register role policies and worker definitions, then call
// SelectWorker to get the best available worker for a given role.
type RoleRouter struct {
	mu       sync.RWMutex
	policies map[string]RolePolicy
	workers  []WorkerDef
}

// NewRoleRouter creates a RoleRouter with the supplied policies and workers.
// Policies are keyed by their Name field; duplicates silently overwrite.
func NewRoleRouter(policies []RolePolicy, workers []WorkerDef) *RoleRouter {
	pm := make(map[string]RolePolicy, len(policies))
	for _, p := range policies {
		pm[p.Name] = p
	}
	wCopy := make([]WorkerDef, len(workers))
	copy(wCopy, workers)
	return &RoleRouter{
		policies: pm,
		workers:  wCopy,
	}
}

// SetPolicy adds or replaces a role policy.
func (r *RoleRouter) SetPolicy(p RolePolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policies[p.Name] = p
}

// SetWorkers replaces the full worker pool.
func (r *RoleRouter) SetWorkers(workers []WorkerDef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workers = make([]WorkerDef, len(workers))
	copy(r.workers, workers)
}

// SetWorkerHealth updates the Healthy flag for the named worker.
// Returns false if the worker is not found.
func (r *RoleRouter) SetWorkerHealth(name string, healthy bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.workers {
		if r.workers[i].Name == name {
			r.workers[i].Healthy = healthy
			return true
		}
	}
	return false
}

// SelectWorker returns the best available worker for the given role.
//
// It walks the role's tier list in preference order and returns the first
// healthy worker whose tier matches. If no healthy worker is found for any
// preferred tier, it returns an error.
func (r *RoleRouter) SelectWorker(role string) (*WorkerDef, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	policy, ok := r.policies[role]
	if !ok {
		return nil, fmt.Errorf("runtime: no policy for role %q", role)
	}

	if len(policy.Tiers) == 0 {
		return nil, fmt.Errorf("runtime: role %q has no cost tiers configured", role)
	}

	for _, tier := range policy.Tiers {
		for i := range r.workers {
			if r.workers[i].Tier == tier && r.workers[i].Healthy {
				w := r.workers[i]
				return &w, nil
			}
		}
	}

	return nil, fmt.Errorf("runtime: no healthy worker for role %q (tried tiers %v)", role, policy.Tiers)
}

// Workers returns a snapshot of the current worker pool.
func (r *RoleRouter) Workers() []WorkerDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]WorkerDef, len(r.workers))
	copy(out, r.workers)
	return out
}

// Policy returns the policy for the named role and whether it exists.
func (r *RoleRouter) Policy(role string) (RolePolicy, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.policies[role]
	return p, ok
}
