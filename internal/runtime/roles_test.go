package runtime

import (
	"strings"
	"testing"
)

func TestSelectWorkerPrefersFirstTier(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{
			Name:  "interactive",
			Tiers: []CostTier{TierPremium, TierCheap},
		}},
		[]WorkerDef{
			{Name: "cheap-1", Tier: TierCheap, Healthy: true},
			{Name: "premium-1", Tier: TierPremium, Healthy: true},
		},
	)

	w, err := router.SelectWorker("interactive")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Name != "premium-1" {
		t.Errorf("SelectWorker(interactive) = %q, want %q", w.Name, "premium-1")
	}
}

func TestSelectWorkerFallsBack(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{
			Name:  "interactive",
			Tiers: []CostTier{TierPremium, TierCheap},
		}},
		[]WorkerDef{
			{Name: "cheap-1", Tier: TierCheap, Healthy: true},
			{Name: "premium-1", Tier: TierPremium, Healthy: false},
		},
	)

	w, err := router.SelectWorker("interactive")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Name != "cheap-1" {
		t.Errorf("SelectWorker(interactive) = %q, want %q (fallback)", w.Name, "cheap-1")
	}
}

func TestSelectWorkerNoHealthy(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{
			Name:  "background",
			Tiers: []CostTier{TierCheap},
		}},
		[]WorkerDef{
			{Name: "cheap-1", Tier: TierCheap, Healthy: false},
		},
	)

	_, err := router.SelectWorker("background")
	if err == nil {
		t.Fatal("expected error when no healthy workers available")
	}
	if !strings.Contains(err.Error(), "no healthy worker") {
		t.Errorf("error = %q, want substring %q", err.Error(), "no healthy worker")
	}
}

func TestSelectWorkerUnknownRole(t *testing.T) {
	router := NewRoleRouter(nil, nil)

	_, err := router.SelectWorker("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown role")
	}
	if !strings.Contains(err.Error(), "no policy") {
		t.Errorf("error = %q, want substring %q", err.Error(), "no policy")
	}
}

func TestSelectWorkerEmptyTiers(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{Name: "empty", Tiers: nil}},
		[]WorkerDef{{Name: "w1", Tier: TierPremium, Healthy: true}},
	)

	_, err := router.SelectWorker("empty")
	if err == nil {
		t.Fatal("expected error for role with empty tiers")
	}
	if !strings.Contains(err.Error(), "no cost tiers") {
		t.Errorf("error = %q, want substring %q", err.Error(), "no cost tiers")
	}
}

func TestSelectWorkerLocalTier(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{
			Name:  "dev",
			Tiers: []CostTier{TierLocal, TierCheap},
		}},
		[]WorkerDef{
			{Name: "ollama", Tier: TierLocal, Healthy: true},
			{Name: "haiku", Tier: TierCheap, Healthy: true},
		},
	)

	w, err := router.SelectWorker("dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Name != "ollama" {
		t.Errorf("SelectWorker(dev) = %q, want %q", w.Name, "ollama")
	}
}

func TestSelectWorkerLocalFallbackToCheap(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{
			Name:  "dev",
			Tiers: []CostTier{TierLocal, TierCheap},
		}},
		[]WorkerDef{
			{Name: "ollama", Tier: TierLocal, Healthy: false},
			{Name: "haiku", Tier: TierCheap, Healthy: true},
		},
	)

	w, err := router.SelectWorker("dev")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Name != "haiku" {
		t.Errorf("SelectWorker(dev) = %q, want %q (fallback)", w.Name, "haiku")
	}
}

func TestSetWorkerHealth(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{
			Name:  "interactive",
			Tiers: []CostTier{TierPremium, TierCheap},
		}},
		[]WorkerDef{
			{Name: "premium-1", Tier: TierPremium, Healthy: true},
			{Name: "cheap-1", Tier: TierCheap, Healthy: true},
		},
	)

	// Premium is preferred and healthy.
	w, _ := router.SelectWorker("interactive")
	if w.Name != "premium-1" {
		t.Fatalf("expected premium-1, got %q", w.Name)
	}

	// Mark premium unhealthy.
	if !router.SetWorkerHealth("premium-1", false) {
		t.Fatal("SetWorkerHealth returned false for existing worker")
	}

	w, _ = router.SelectWorker("interactive")
	if w.Name != "cheap-1" {
		t.Errorf("after marking premium unhealthy, got %q, want cheap-1", w.Name)
	}

	// Unknown worker.
	if router.SetWorkerHealth("nonexistent", true) {
		t.Error("SetWorkerHealth returned true for unknown worker")
	}
}

func TestSetPolicy(t *testing.T) {
	router := NewRoleRouter(nil, []WorkerDef{
		{Name: "premium-1", Tier: TierPremium, Healthy: true},
	})

	_, err := router.SelectWorker("new-role")
	if err == nil {
		t.Fatal("expected error before policy is set")
	}

	router.SetPolicy(RolePolicy{
		Name:  "new-role",
		Tiers: []CostTier{TierPremium},
	})

	w, err := router.SelectWorker("new-role")
	if err != nil {
		t.Fatalf("unexpected error after SetPolicy: %v", err)
	}
	if w.Name != "premium-1" {
		t.Errorf("got %q, want premium-1", w.Name)
	}
}

func TestSetWorkers(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{
			Name:  "bg",
			Tiers: []CostTier{TierCheap},
		}},
		nil,
	)

	_, err := router.SelectWorker("bg")
	if err == nil {
		t.Fatal("expected error with no workers")
	}

	router.SetWorkers([]WorkerDef{
		{Name: "flash", Tier: TierCheap, Healthy: true},
	})

	w, err := router.SelectWorker("bg")
	if err != nil {
		t.Fatalf("unexpected error after SetWorkers: %v", err)
	}
	if w.Name != "flash" {
		t.Errorf("got %q, want flash", w.Name)
	}
}

func TestWorkersSnapshot(t *testing.T) {
	original := []WorkerDef{
		{Name: "a", Tier: TierPremium, Healthy: true},
		{Name: "b", Tier: TierCheap, Healthy: false},
	}
	router := NewRoleRouter(nil, original)

	snap := router.Workers()
	if len(snap) != 2 {
		t.Fatalf("Workers() returned %d items, want 2", len(snap))
	}

	// Mutating snapshot must not affect router.
	snap[0].Healthy = false
	snap2 := router.Workers()
	if !snap2[0].Healthy {
		t.Error("mutating snapshot affected router state")
	}
}

func TestPolicyLookup(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{Name: "admin", Tiers: []CostTier{TierPremium}}},
		nil,
	)

	p, ok := router.Policy("admin")
	if !ok {
		t.Fatal("Policy(admin) not found")
	}
	if p.Name != "admin" {
		t.Errorf("Policy.Name = %q, want admin", p.Name)
	}

	_, ok = router.Policy("missing")
	if ok {
		t.Error("Policy(missing) should return false")
	}
}

func TestValidCostTiers(t *testing.T) {
	for _, tier := range []CostTier{TierPremium, TierCheap, TierLocal} {
		if !ValidCostTiers[tier] {
			t.Errorf("ValidCostTiers[%q] = false, want true", tier)
		}
	}
	if ValidCostTiers["unknown"] {
		t.Error("ValidCostTiers[unknown] = true, want false")
	}
}

func TestDuplicatePolicyOverwrites(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{
			{Name: "role1", Tiers: []CostTier{TierCheap}},
			{Name: "role1", Tiers: []CostTier{TierPremium}},
		},
		[]WorkerDef{
			{Name: "premium-1", Tier: TierPremium, Healthy: true},
			{Name: "cheap-1", Tier: TierCheap, Healthy: true},
		},
	)

	w, err := router.SelectWorker("role1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Last policy wins.
	if w.Name != "premium-1" {
		t.Errorf("got %q, want premium-1 (last policy should win)", w.Name)
	}
}

func TestSelectWorkerReturnsFirstMatchInTier(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{
			Name:  "batch",
			Tiers: []CostTier{TierCheap},
		}},
		[]WorkerDef{
			{Name: "cheap-a", Tier: TierCheap, Healthy: true},
			{Name: "cheap-b", Tier: TierCheap, Healthy: true},
		},
	)

	w, err := router.SelectWorker("batch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.Name != "cheap-a" {
		t.Errorf("got %q, want cheap-a (first match in tier)", w.Name)
	}
}

func TestMaxConcurrentFieldPreserved(t *testing.T) {
	router := NewRoleRouter(
		[]RolePolicy{{
			Name:          "limited",
			Tiers:         []CostTier{TierPremium},
			MaxConcurrent: 5,
		}},
		nil,
	)

	p, ok := router.Policy("limited")
	if !ok {
		t.Fatal("policy not found")
	}
	if p.MaxConcurrent != 5 {
		t.Errorf("MaxConcurrent = %d, want 5", p.MaxConcurrent)
	}
}
