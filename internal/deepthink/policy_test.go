package deepthink

import (
	"strings"
	"testing"
)

func testPolicy() *Policy {
	return NewPolicy(PolicyOptions{
		Tiers: []TierTarget{
			{Name: "premium", Model: "gpt-6-sol", Thinking: "high"},
			{Name: "standard", Model: "gpt-6-sol", Thinking: "medium"},
		},
		Models:          []string{"sol"},
		OnRequestModels: []string{"claude-opus-5-5", "claude-fable-5-1", "gpt-6-astra"},
		Aliases:         map[string]string{"sol": "gpt-6-sol", "opus": "claude-opus-5-5", "fable": "claude-fable-5-1"},
		ModelThinking:   "high",
	})
}

func TestPolicyDefaultsToFirstTier(t *testing.T) {
	got, err := testPolicy().Resolve(Request{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != "premium" || got.Model != "gpt-6-sol" || got.Thinking != "high" {
		t.Fatalf("got %+v", got)
	}
}

func TestPolicyResolvesTierCaseInsensitively(t *testing.T) {
	got, err := testPolicy().Resolve(Request{Tier: " Standard "})
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != "standard" || got.Thinking != "medium" {
		t.Fatalf("got %+v", got)
	}
}

func TestPolicyRejectsUnknownTierAndModel(t *testing.T) {
	p := testPolicy()
	if _, err := p.Resolve(Request{Tier: "cheap"}); err == nil || !strings.Contains(err.Error(), "allowed tiers: premium, standard") {
		t.Fatalf("unknown tier error = %v", err)
	}
	if _, err := p.Resolve(Request{Model: "gpt-3.5"}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unknown model error = %v", err)
	}
	if _, err := p.Resolve(Request{Tier: "premium", Model: "gpt-6-sol"}); err == nil {
		t.Fatal("tier+model together must be rejected")
	}
}

func TestPolicyAllowsListedModelThroughAlias(t *testing.T) {
	got, err := testPolicy().Resolve(Request{Model: "SOL"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gpt-6-sol" || got.Tier != "" || got.Thinking != "high" {
		t.Fatalf("got %+v", got)
	}
}

func TestPolicyStrongModelRequiresExplicitUserRequest(t *testing.T) {
	p := testPolicy()

	_, err := p.Resolve(Request{Model: "claude-opus-5-5", UserMessage: "объясни теорему Гёделя"})
	if err == nil || !strings.Contains(err.Error(), "explicitly asks") {
		t.Fatalf("strong model without request: err = %v", err)
	}
	if _, err := p.Resolve(Request{Model: "opus", UserMessage: ""}); err == nil {
		t.Fatal("strong model with empty user message must be rejected")
	}

	for _, msg := range []string{
		"объясни теорему Гёделя, спроси у claude-opus-5-5",
		"Спроси Opus: объясни теорему Гёделя",
		"use CLAUDE-OPUS-5-5 for this one",
	} {
		got, err := p.Resolve(Request{Model: "claude-opus-5-5", UserMessage: msg})
		if err != nil {
			t.Fatalf("%q: %v", msg, err)
		}
		if got.Model != "claude-opus-5-5" || got.Thinking != "high" {
			t.Fatalf("%q: got %+v", msg, got)
		}
	}

	// The tool may pass the alias; the check still uses the user's wording.
	if _, err := p.Resolve(Request{Model: "fable", UserMessage: "ask fable please"}); err != nil {
		t.Fatalf("alias in user message: %v", err)
	}
}

func TestPolicyNilWhenNothingAllowed(t *testing.T) {
	if p := NewPolicy(PolicyOptions{}); p != nil {
		t.Fatalf("expected nil policy, got %+v", p)
	}
	var nilPolicy *Policy
	if _, err := nilPolicy.Resolve(Request{}); err == nil {
		t.Fatal("nil policy must refuse")
	}
	if nilPolicy.Describe() == "" {
		t.Fatal("nil policy description empty")
	}
}

func TestPolicyDescribeListsAllowlist(t *testing.T) {
	d := testPolicy().Describe()
	for _, want := range []string{"premium, standard", "models: gpt-6-sol", "claude-fable-5-1, claude-opus-5-5, gpt-6-astra"} {
		if !strings.Contains(d, want) {
			t.Errorf("Describe() = %q, missing %q", d, want)
		}
	}
}
