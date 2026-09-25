package bot

import (
	"fmt"
	"log"
	"strings"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/config"
	"ok-gobot/internal/deepthink"
	"ok-gobot/internal/tools"
)

// deepThinkTrigger promotes a single chat turn when a configured phrase opens
// or closes the message. It is deterministic: no model call decides anything.
type deepThinkTrigger struct {
	matcher  *deepthink.Matcher
	tier     string
	thinking string
}

// deepThinkEscalation records a successful deep_think tool call in the
// current run, for the reply indicator.
type deepThinkEscalation struct {
	Tier     string
	Model    string
	Thinking string
}

// SetDeepThink wires ai.deep_think. It must run after SetWorkerSelector: tier
// targets are resolved through runtime.cost_tiers. A zero config leaves both
// the trigger and the tool off.
func (b *Bot) SetDeepThink(cfg config.DeepThinkConfig) {
	matcher := deepthink.NewMatcher(cfg.Triggers)
	tier := strings.ToLower(strings.TrimSpace(cfg.Tier))
	thinking := strings.TrimSpace(cfg.Thinking)
	if matcher.Enabled() && (tier != "" || thinking != "") {
		b.deepThink = &deepThinkTrigger{matcher: matcher, tier: tier, thinking: thinking}
		log.Printf("🧠 Deep-think trigger enabled: %d phrases → tier=%q thinking=%q", len(matcher.Phrases()), tier, thinking)
	} else {
		b.deepThink = nil
	}

	if b.resolver == nil {
		return
	}
	b.resolver.DeepThink = nil
	if !cfg.Escalation.Enabled {
		return
	}
	var tiers []deepthink.TierTarget
	for _, name := range cfg.Escalation.Tiers {
		label, tierCfg, ok := b.resolveJobTier(name)
		if !ok || strings.TrimSpace(tierCfg.Model) == "" {
			log.Printf("⚠️ [deep_think] escalation tier %q has no model in runtime.cost_tiers, skipped", name)
			continue
		}
		tiers = append(tiers, deepthink.TierTarget{Name: label, Model: b.resolveModelAlias(tierCfg.Model), Thinking: tierCfg.Thinking})
	}
	modelThinking := thinking
	if modelThinking == "" && len(tiers) > 0 {
		modelThinking = tiers[0].Thinking
	}
	if modelThinking == "" {
		modelThinking = "high"
	}
	policy := deepthink.NewPolicy(deepthink.PolicyOptions{
		Tiers:           tiers,
		Models:          cfg.Escalation.Models,
		OnRequestModels: cfg.Escalation.OnRequestModels,
		Aliases:         b.getModelAliases(),
		ModelThinking:   modelThinking,
	})
	if policy == nil {
		log.Printf("⚠️ [deep_think] escalation enabled but nothing resolvable is allowed; tool not registered")
		return
	}
	b.resolver.DeepThink = policy
	log.Printf("🧠 deep_think tool enabled: %s", policy.Describe())
}

// deepThinkTurn checks a plain text chat turn for a trigger phrase. On a
// match it returns the request without the phrase and hard RunOverrides for
// this one turn (above session /model and /think, past any fast lane);
// otherwise the input is returned unchanged with nil overrides.
func (b *Bot) deepThinkTurn(chatID int64, content string) (string, *agent.RunOverrides) {
	trigger := b.deepThink
	if trigger == nil {
		return content, nil
	}
	match, ok := trigger.matcher.Match(content)
	if !ok {
		return content, nil
	}

	overrides := &agent.RunOverrides{DeepThink: true, ThinkLevel: trigger.thinking}
	if trigger.tier != "" {
		label, tierCfg, tiered := b.resolveJobTier(trigger.tier)
		switch {
		case tiered && strings.TrimSpace(tierCfg.Model) != "":
			overrides.Model = b.resolveModelAlias(tierCfg.Model)
			overrides.Tier = label
			if overrides.ThinkLevel == "" {
				overrides.ThinkLevel = tierCfg.Thinking
			}
		case overrides.ThinkLevel != "":
			log.Printf("[deep_think] tier %q unresolved for chat=%d, raising thinking only", trigger.tier, chatID)
		default:
			log.Printf("[deep_think] tier %q unresolved for chat=%d and no thinking configured, turn runs unpromoted", trigger.tier, chatID)
			return content, nil
		}
	}
	log.Printf("[deep_think] trigger phrase=%q chat=%d tier=%s model=%s thinking=%s",
		match.Phrase, chatID, overrides.Tier, overrides.Model, overrides.ThinkLevel)
	return match.Rest, overrides
}

// alignTesseraTurnWithDeepThink keeps Tessera turn authority on a promoted
// turn: tesseraRunContext grants it only when the turn text equals the run
// content, and the trigger phrase was just removed from that content.
func (b *Bot) alignTesseraTurnWithDeepThink(d *telegramDelivery, content string, overrides *agent.RunOverrides) {
	if d == nil || d.Turn == nil || overrides == nil || !overrides.DeepThink || b.deepThink == nil {
		return
	}
	if match, ok := b.deepThink.matcher.Match(d.Turn.Content); ok && match.Rest == content {
		d.Turn.Content = content
	}
}

// deepThinkEscalationFromEvent extracts a successful deep_think result.
func deepThinkEscalationFromEvent(event agent.ToolEvent) *deepThinkEscalation {
	if event.Type != agent.ToolEventFinished || event.ToolName != "deep_think" || event.Err != nil || event.Denial != nil {
		return nil
	}
	output := event.FullOutput
	if output == "" {
		output = event.Output
	}
	tier, model, thinking, ok := tools.ParseDeepThinkHeader(output)
	if !ok {
		return nil
	}
	return &deepThinkEscalation{Tier: tier, Model: model, Thinking: thinking}
}

// deepThinkFooter renders the reply indicator: "🧠 gpt-6-sol · high" for a
// triggered turn (from the run's actual model/effort, so a preflight
// fallback shows what really ran), "🧠 deep_think → gpt-6-sol · high" when
// the model escalated through the tool. Both may appear.
func deepThinkFooter(overrides *agent.RunOverrides, info agent.RunStartInfo, esc *deepThinkEscalation) string {
	var lines []string
	if overrides != nil && overrides.DeepThink && info.Model != "" {
		lines = append(lines, "🧠 "+formatModelEffort(info.Model, info.Effort))
	}
	if esc != nil && esc.Model != "" {
		lines = append(lines, "🧠 deep_think → "+formatModelEffort(esc.Model, esc.Thinking))
	}
	return strings.Join(lines, "\n")
}

func formatModelEffort(model, effort string) string {
	if effort == "" {
		return model
	}
	return fmt.Sprintf("%s · %s", model, effort)
}
