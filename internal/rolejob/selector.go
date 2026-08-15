package rolejob

import (
	"fmt"
	"log"
	"strings"
	"time"

	"ok-gobot/internal/config"
	"ok-gobot/internal/role"
	jobruntime "ok-gobot/internal/runtime"
)

// ResolveTier resolves the cost tier for a role run. An explicit worker/tier
// request (manifest worker: field or CLI --tier via opts.Worker) wins;
// without one, only a configured role policy opts the run in — honoring its
// default_tier. Legacy manifests without worker: and without a role policy
// stay untiered. Unknown tier names log and run untiered instead of being
// silently coerced.
func ResolveTier(m *role.Manifest, opts Options) (jobruntime.CostTier, jobruntime.TierConfig, bool) {
	if opts.Selector == nil || m == nil {
		return "", jobruntime.TierConfig{}, false
	}
	worker := strings.TrimSpace(opts.Worker)
	if worker == "" {
		worker = strings.TrimSpace(m.Worker)
	}
	if worker != "" {
		requested, ok := jobruntime.ParseCostTier(worker)
		if !ok {
			log.Printf("[tiers] role %s: unknown worker tier %q, running untiered", m.Name, worker)
			return "", jobruntime.TierConfig{}, false
		}
		resolved, tierCfg, err := opts.Selector.Resolve(m.Name, requested)
		if err != nil {
			log.Printf("[tiers] role %s: tier %q unresolved, running untiered: %v", m.Name, requested, err)
			return "", jobruntime.TierConfig{}, false
		}
		return resolved, tierCfg, true
	}
	if opts.Selector.Role(m.Name) == nil {
		return "", jobruntime.TierConfig{}, false
	}
	resolved, tierCfg, err := opts.Selector.ResolveForRole(m.Name)
	if err != nil {
		log.Printf("[tiers] role %s: default tier unresolved, running untiered: %v", m.Name, err)
		return "", jobruntime.TierConfig{}, false
	}
	return resolved, tierCfg, true
}

// SelectorFromConfig builds the tier selector from the validated
// runtime.cost_tiers / runtime.roles config sections. A config without any
// tiers or role policies yields a nil selector: the feature stays off.
func SelectorFromConfig(cfg config.RuntimeConfig) (*jobruntime.WorkerSelector, error) {
	if len(cfg.CostTiers) == 0 && len(cfg.Roles) == 0 {
		return nil, nil
	}

	globals, err := tierConfigsFromEntries(cfg.CostTiers)
	if err != nil {
		return nil, err
	}

	roles := make([]*jobruntime.RolePolicy, 0, len(cfg.Roles))
	for _, entry := range cfg.Roles {
		tiers, err := tierConfigsFromEntries(entry.Tiers)
		if err != nil {
			return nil, fmt.Errorf("role %q: %w", entry.Name, err)
		}
		defaultTier, ok := jobruntime.ParseCostTier(entry.DefaultTier)
		if !ok {
			return nil, fmt.Errorf("role %q: unknown default tier %q", entry.Name, entry.DefaultTier)
		}
		roles = append(roles, &jobruntime.RolePolicy{
			Name:        entry.Name,
			DefaultTier: defaultTier,
			Tiers:       tiers,
		})
	}

	return jobruntime.NewWorkerSelector(globals, roles), nil
}

func tierConfigsFromEntries(entries map[string]config.CostTierEntry) (map[jobruntime.CostTier]jobruntime.TierConfig, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make(map[jobruntime.CostTier]jobruntime.TierConfig, len(entries))
	for name, entry := range entries {
		tier, ok := jobruntime.ParseCostTier(name)
		if !ok {
			return nil, fmt.Errorf("unknown cost tier %q", name)
		}
		var maxDuration time.Duration
		if entry.MaxDuration != "" {
			parsed, err := time.ParseDuration(entry.MaxDuration)
			if err != nil {
				return nil, fmt.Errorf("cost tier %q: invalid max_duration: %w", name, err)
			}
			maxDuration = parsed
		}
		out[tier] = jobruntime.TierConfig{
			Model:        entry.Model,
			Provider:     entry.Provider,
			BaseURL:      entry.BaseURL,
			Thinking:     entry.Thinking,
			MaxToolCalls: entry.MaxToolCalls,
			MaxDuration:  maxDuration,
		}
	}
	return out, nil
}
