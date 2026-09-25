package bot

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
	"ok-gobot/internal/runtime"
)

// preflightRecorder observes which model/effort each run resolved to. In the
// policy fixture the provider is "test", so buildAIClient always falls back
// to the stub DefaultClient: preflight is the only place the chosen model is
// visible to a test.
type preflightRecorder struct {
	mu    sync.Mutex
	calls []preflightCall
	fail  map[string]error
}

type preflightCall struct{ model, tier, effort string }

func (p *preflightRecorder) check(_ context.Context, model, tier, effort string) (ai.BackendHealth, error) {
	p.mu.Lock()
	p.calls = append(p.calls, preflightCall{model, tier, effort})
	p.mu.Unlock()
	if err, ok := p.fail[model]; ok {
		return ai.BackendHealth{}, err
	}
	return ai.BackendHealth{Status: ai.BackendHealthHealthy}, nil
}

func (p *preflightRecorder) last() preflightCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		return preflightCall{}
	}
	return p.calls[len(p.calls)-1]
}

func (c *policyAIClient) lastUserText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) == 0 {
		return ""
	}
	return c.seen[len(c.seen)-1]
}

var deepThinkTestConfig = config.DeepThinkConfig{
	Triggers: []string{"подумай хорошо", "think hard"},
	Tier:     "premium",
	Escalation: config.DeepThinkEscalationConfig{
		Enabled:         true,
		Tiers:           []string{"premium"},
		OnRequestModels: []string{"claude-opus-5-5"},
	},
}

func newDeepThinkTestBot(t *testing.T, tg *fakeTelegramAPI, cfg config.DeepThinkConfig) (*Bot, *policyAIClient, *preflightRecorder) {
	t.Helper()
	b, aiClient := newChatPolicyTestBot(t, tg, "standby")
	pre := &preflightRecorder{fail: map[string]error{}}
	b.resolver.AIConfig.BackendPreflight = pre.check
	b.SetWorkerSelector(runtime.NewWorkerSelector(map[runtime.CostTier]runtime.TierConfig{
		runtime.CostTierPremium: {Model: "premium-model", Thinking: "high"},
	}, nil))
	b.SetDeepThink(cfg)
	return b, aiClient, pre
}

// runDeepThinkTurn sends one DM, waits for the run to finish and returns
// every text the bot sent or edited (ack, streaming edits, final reply).
func runDeepThinkTurn(t *testing.T, b *Bot, tg *fakeTelegramAPI, text string) string {
	t.Helper()
	if err := b.handleMessage(context.Background(), dmContext(100, text)); err != nil {
		t.Fatalf("handleMessage() error = %v", err)
	}
	tg.waitForText(t, policyAnswer, 5*time.Second)
	waitForChatIdle(t, b, 5150, 5*time.Second)
	tg.mu.Lock()
	defer tg.mu.Unlock()
	var out strings.Builder
	for _, req := range tg.requests {
		out.WriteString(req.Text)
		out.WriteString("\n")
	}
	return out.String()
}

func TestDeepThinkTriggerPromotesTurnToPremium(t *testing.T) {
	for _, text := range []string{
		"Подумай хорошо: сколько будет 2+2?",
		"сколько будет 2+2? think hard",
	} {
		t.Run(text, func(t *testing.T) {
			tg := newFakeTelegramAPI(t)
			b, aiClient, pre := newDeepThinkTestBot(t, tg, deepThinkTestConfig)

			reply := runDeepThinkTurn(t, b, tg, text)

			if got := pre.last(); got != (preflightCall{"premium-model", "premium", "high"}) {
				t.Fatalf("resolved run = %+v, want premium-model/premium/high", got)
			}
			if got := aiClient.lastUserText(); got != "сколько будет 2+2?" {
				t.Fatalf("model saw %q, want the request without the phrase", got)
			}
			if !strings.Contains(reply, "🧠 premium-model · high") {
				t.Fatalf("reply lacks indicator: %q", reply)
			}
		})
	}
}

func TestDeepThinkTriggerIgnoresPhraseMidSentenceAndCommands(t *testing.T) {
	for _, text := range []string{
		"я не подумал хорошо о том, сколько будет 2+2",
		"/media_request Dune think hard",
	} {
		t.Run(text, func(t *testing.T) {
			tg := newFakeTelegramAPI(t)
			b, aiClient, pre := newDeepThinkTestBot(t, tg, deepThinkTestConfig)
			if strings.HasPrefix(text, "/") {
				b, aiClient, _ = newSkillCommandTestBot(t, tg)
				pre = &preflightRecorder{fail: map[string]error{}}
				b.resolver.AIConfig.BackendPreflight = pre.check
				b.SetWorkerSelector(runtime.NewWorkerSelector(map[runtime.CostTier]runtime.TierConfig{
					runtime.CostTierPremium: {Model: "premium-model", Thinking: "high"},
				}, nil))
				b.SetDeepThink(deepThinkTestConfig)
			}

			reply := runDeepThinkTurn(t, b, tg, text)

			if got := pre.last(); got.model != "gpt-4o" || got.tier == "premium" {
				t.Fatalf("resolved run = %+v, want default model", got)
			}
			if strings.Contains(reply, "🧠") {
				t.Fatalf("unexpected indicator in reply: %q", reply)
			}
			if strings.HasPrefix(text, "/") {
				if !aiClient.sawUserText("User request:\nDune think hard") {
					t.Fatalf("skill command args were rewritten; seen=%q", aiClient.seen)
				}
			} else if got := aiClient.lastUserText(); got != text {
				t.Fatalf("model saw %q, want the untouched text", got)
			}
		})
	}
}

func TestDeepThinkTriggerDegradesWhenTargetFailsPreflight(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, _, pre := newDeepThinkTestBot(t, tg, deepThinkTestConfig)
	pre.fail["premium-model"] = errors.New("model retired")

	reply := runDeepThinkTurn(t, b, tg, "think hard: сколько будет 2+2?")

	if got := pre.last(); got.model != "gpt-4o" {
		t.Fatalf("resolved run = %+v, want default lane", got)
	}
	// The indicator is honest: it names what actually ran.
	if !strings.Contains(reply, "🧠 gpt-4o") || strings.Contains(reply, "premium-model") {
		t.Fatalf("reply indicator = %q", reply)
	}
}

func TestDeepThinkOffByDefault(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, aiClient, pre := newDeepThinkTestBot(t, tg, config.DeepThinkConfig{})

	if b.deepThink != nil || b.resolver.DeepThink != nil {
		t.Fatal("zero config must leave trigger and tool off")
	}
	reply := runDeepThinkTurn(t, b, tg, "think hard: сколько будет 2+2?")

	if got := pre.last(); got.model != "gpt-4o" {
		t.Fatalf("resolved run = %+v, want default", got)
	}
	if got := aiClient.lastUserText(); got != "think hard: сколько будет 2+2?" {
		t.Fatalf("model saw %q, want untouched text", got)
	}
	if strings.Contains(reply, "🧠") {
		t.Fatalf("unexpected indicator: %q", reply)
	}
}

func TestSetDeepThinkBuildsEscalationPolicyFromTiers(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, _, _ := newDeepThinkTestBot(t, tg, deepThinkTestConfig)

	policy := b.resolver.DeepThink
	if policy == nil {
		t.Fatal("escalation policy not wired")
	}
	if got := policy.Tiers(); len(got) != 1 || got[0] != "premium" {
		t.Fatalf("tiers = %v", got)
	}
	if got := policy.OnRequestModels(); len(got) != 1 || got[0] != "claude-opus-5-5" {
		t.Fatalf("on-request models = %v", got)
	}

	// Trigger-only config leaves the tool off.
	b.SetDeepThink(config.DeepThinkConfig{Triggers: []string{"think hard"}, Tier: "premium"})
	if b.resolver.DeepThink != nil || b.deepThink == nil {
		t.Fatal("trigger-only config must keep the tool off and the trigger on")
	}
}

func TestDeepThinkFooter(t *testing.T) {
	info := agent.RunStartInfo{Model: "gpt-6-sol", Effort: "high"}
	if got := deepThinkFooter(&agent.RunOverrides{DeepThink: true}, info, nil); got != "🧠 gpt-6-sol · high" {
		t.Fatalf("trigger footer = %q", got)
	}
	if got := deepThinkFooter(nil, info, nil); got != "" {
		t.Fatalf("plain turn footer = %q", got)
	}
	esc := &deepThinkEscalation{Tier: "premium", Model: "gpt-6-sol", Thinking: "high"}
	if got := deepThinkFooter(nil, info, esc); got != "🧠 deep_think → gpt-6-sol · high" {
		t.Fatalf("escalation footer = %q", got)
	}
	if got := deepThinkFooter(&agent.RunOverrides{DeepThink: true}, agent.RunStartInfo{Model: "m"}, &deepThinkEscalation{Model: "x"}); got != "🧠 m\n🧠 deep_think → x" {
		t.Fatalf("combined footer = %q", got)
	}

	ok := agent.ToolEvent{ToolName: "deep_think", Type: agent.ToolEventFinished, FullOutput: "deep_think: tier=premium model=gpt-6-sol thinking=high\n\nanswer"}
	if got := deepThinkEscalationFromEvent(ok); got == nil || got.Model != "gpt-6-sol" || got.Thinking != "high" {
		t.Fatalf("escalation from event = %+v", got)
	}
	failed := ok
	failed.Err = errors.New("boom")
	if deepThinkEscalationFromEvent(failed) != nil {
		t.Fatal("failed escalation must not produce an indicator")
	}
	other := ok
	other.ToolName = "host_task"
	if deepThinkEscalationFromEvent(other) != nil {
		t.Fatal("other tools must not produce an indicator")
	}
}
