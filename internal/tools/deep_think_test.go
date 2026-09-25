package tools

import (
	"context"
	"strings"
	"testing"

	"ok-gobot/internal/deepthink"
	"ok-gobot/internal/delegation"
)

func testDeepThinkPolicy() *deepthink.Policy {
	return deepthink.NewPolicy(deepthink.PolicyOptions{
		Tiers:           []deepthink.TierTarget{{Name: "premium", Model: "gpt-6-sol", Thinking: "high"}},
		OnRequestModels: []string{"claude-opus-5-5"},
		Aliases:         map[string]string{"opus": "claude-opus-5-5"},
		ModelThinking:   "high",
	})
}

func TestDeepThinkEscalatesToDefaultTier(t *testing.T) {
	sub := &jobCapturingSubmitter{}
	tool := NewDeepThinkTool(sub, 42, testDeepThinkPolicy())
	ctx := WithUserMessage(context.Background(), "Объясни, почему небо синее, подробно")

	out, err := tool.ExecuteJSON(ctx, map[string]string{
		"request": "Explain in depth why the sky is blue.",
		"reason":  "multi-step physics explanation",
	})
	if err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if !sub.called {
		t.Fatal("submitter not called")
	}
	if sub.job.Model != "gpt-6-sol" || sub.job.Thinking != "high" {
		t.Fatalf("job model/thinking = %q/%q, want gpt-6-sol/high", sub.job.Model, sub.job.Thinking)
	}
	if sub.job.MemoryPolicy != delegation.MemoryPolicyReadOnly {
		t.Fatalf("memory policy = %q, want read_only", sub.job.MemoryPolicy)
	}
	for _, forbidden := range []string{"exec", "browser", "host_task", "browser_task", "deep_think"} {
		for _, allowed := range sub.job.ToolAllowlist {
			if allowed == forbidden {
				t.Fatalf("worker allowlist contains %q", forbidden)
			}
		}
	}
	for _, want := range []string{"Explain in depth why the sky is blue.", "Объясни, почему небо синее, подробно"} {
		if !strings.Contains(sub.prompt, want) {
			t.Fatalf("worker prompt missing %q:\n%s", want, sub.prompt)
		}
	}
	tier, model, thinking, ok := ParseDeepThinkHeader(out)
	if !ok || tier != "premium" || model != "gpt-6-sol" || thinking != "high" {
		t.Fatalf("header = %q/%q/%q ok=%v; output:\n%s", tier, model, thinking, ok, out)
	}
	if !strings.HasSuffix(out, "\n\nok") {
		t.Fatalf("worker result not appended: %q", out)
	}
}

func TestDeepThinkStrongModelOnlyWhenUserAsked(t *testing.T) {
	sub := &jobCapturingSubmitter{}
	tool := NewDeepThinkTool(sub, 42, testDeepThinkPolicy())

	ctx := WithUserMessage(context.Background(), "сравни два подхода к кэшированию")
	_, err := tool.ExecuteJSON(ctx, map[string]string{"request": "compare", "reason": "hard", "model": "claude-opus-5-5"})
	if err == nil || !strings.Contains(err.Error(), "explicitly asks") {
		t.Fatalf("strong model without request: err = %v", err)
	}
	if sub.called {
		t.Fatal("refused escalation reached the submitter")
	}

	// A refusal does not burn the per-turn budget; the retry with an allowed tier runs.
	if _, err := tool.ExecuteJSON(ctx, map[string]string{"request": "compare", "reason": "hard", "tier": "premium"}); err != nil {
		t.Fatalf("retry with tier: %v", err)
	}
	if sub.job.Model != "gpt-6-sol" {
		t.Fatalf("retry model = %q", sub.job.Model)
	}

	sub2 := &jobCapturingSubmitter{}
	tool2 := NewDeepThinkTool(sub2, 42, testDeepThinkPolicy())
	ctx2 := WithUserMessage(context.Background(), "спроси у opus: сравни два подхода к кэшированию")
	if _, err := tool2.ExecuteJSON(ctx2, map[string]string{"request": "compare", "reason": "user asked for opus", "model": "opus"}); err != nil {
		t.Fatalf("strong model with request: %v", err)
	}
	if sub2.job.Model != "claude-opus-5-5" || sub2.job.Thinking != "high" {
		t.Fatalf("job = %q/%q", sub2.job.Model, sub2.job.Thinking)
	}
}

func TestDeepThinkRunsAtMostOncePerTurn(t *testing.T) {
	sub := &jobCapturingSubmitter{}
	tool := NewDeepThinkTool(sub, 42, testDeepThinkPolicy())
	params := map[string]string{"request": "q", "reason": "r"}

	if _, err := tool.ExecuteJSON(context.Background(), params); err != nil {
		t.Fatalf("first call: %v", err)
	}
	_, err := tool.ExecuteJSON(context.Background(), params)
	if err == nil || !strings.Contains(err.Error(), "already ran once") {
		t.Fatalf("second call err = %v, want per-turn limit", err)
	}
}

func TestDeepThinkRejectsUnknownTierAndMissingRequest(t *testing.T) {
	tool := NewDeepThinkTool(&jobCapturingSubmitter{}, 42, testDeepThinkPolicy())
	if _, err := tool.ExecuteJSON(context.Background(), map[string]string{"reason": "r"}); err == nil {
		t.Fatal("missing request accepted")
	}
	if _, err := tool.ExecuteJSON(context.Background(), map[string]string{"request": "q", "reason": "r", "tier": "cheap"}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("unknown tier err = %v", err)
	}
}

func TestParseDeepThinkHeader(t *testing.T) {
	if _, _, _, ok := ParseDeepThinkHeader("plain tool output"); ok {
		t.Fatal("plain output parsed as header")
	}
	tier, model, thinking, ok := ParseDeepThinkHeader(FormatDeepThinkHeader("model", "claude-opus-5-5", "") + "\n\nanswer")
	if !ok || tier != "model" || model != "claude-opus-5-5" || thinking != "" {
		t.Fatalf("got %q/%q/%q ok=%v", tier, model, thinking, ok)
	}
}

func TestDeepThinkDescriptionListsAllowedTargets(t *testing.T) {
	d := NewDeepThinkTool(nil, 1, testDeepThinkPolicy()).Description()
	for _, want := range []string{"once per turn", "premium", "claude-opus-5-5", "does not see this conversation"} {
		if !strings.Contains(d, want) {
			t.Errorf("description missing %q", want)
		}
	}
}
