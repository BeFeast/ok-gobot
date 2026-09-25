package tools

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"ok-gobot/internal/deepthink"
	"ok-gobot/internal/delegation"
)

// DeepThinkOutputPrefix opens the tool result. The bot reads this first line
// from the finished tool event to render the "🧠 deep_think → …" indicator.
const DeepThinkOutputPrefix = "deep_think:"

// DeepThinkTool lets the main agent hand the current request to a stronger
// tier or an explicitly requested model. It mirrors host_task/browser_task:
// a fresh sub-agent run through SubagentSubmitter, bounded by a delegation
// contract. Guardrails are deterministic and live in deepthink.Policy: only
// configured tiers and models, strong models only when the user's own message
// names them, and one escalation per turn (the registry is rebuilt per run, so
// the instance counter is the per-turn budget). Sub-agents never receive the
// tool, so an escalation cannot escalate again.
type DeepThinkTool struct {
	submitter SubagentSubmitter
	chatID    int64
	policy    *deepthink.Policy
	used      atomic.Bool
}

func NewDeepThinkTool(submitter SubagentSubmitter, chatID int64, policy *deepthink.Policy) *DeepThinkTool {
	return &DeepThinkTool{submitter: submitter, chatID: chatID, policy: policy}
}

func (t *DeepThinkTool) Name() string { return "deep_think" }

// OwnsTimeout: the tool blocks on SubmitAndWait, which enforces the worker's
// own MaxDuration, so the generic per-tool timeout must not apply.
func (t *DeepThinkTool) OwnsTimeout() bool { return true }

func (t *DeepThinkTool) Description() string {
	return "Hand the current request to a stronger reasoning tier and return its answer. Use it when the " +
		"request needs hard analysis, multi-step reasoning, careful comparison of options, or when the user " +
		"asks you to think carefully; do not use it for chit-chat, lookups, or tasks a tool already answers. " +
		"The worker does not see this conversation: put everything it needs (the user's question verbatim, " +
		"relevant facts, constraints, prior answers) into `request`. Call it at most once per turn, then " +
		"deliver its answer to the user instead of re-deriving it. Leave `tier` and `model` empty for the " +
		"default tier; name a specific model only when the user explicitly asked for that model. " +
		"Allowed targets: " + t.policy.Describe() + "."
}

func (t *DeepThinkTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("request required")
	}
	return t.run(ctx, args[0], "", "", "")
}

func (t *DeepThinkTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	request := firstNonEmptyParam(params, "request", "task", "input", "question")
	if request == "" {
		return "", fmt.Errorf("'request' is required")
	}
	return t.run(ctx, request, params["reason"], params["tier"], params["model"])
}

func (t *DeepThinkTool) run(ctx context.Context, request, reason, tier, model string) (string, error) {
	if t.submitter == nil {
		return "", fmt.Errorf("subagent submitter not configured")
	}
	if t.policy == nil {
		return "", fmt.Errorf("deep_think is not configured")
	}
	if !t.used.CompareAndSwap(false, true) {
		return "", fmt.Errorf("deep_think already ran once in this turn; answer with the result you have")
	}

	userMessage := UserMessageFromContext(ctx)
	target, err := t.policy.Resolve(deepthink.Request{Tier: tier, Model: model, UserMessage: userMessage})
	if err != nil {
		// A refused target does not consume the budget: the model may retry
		// with an allowed tier.
		t.used.Store(false)
		return "", fmt.Errorf("deep_think refused: %w", err)
	}

	tierLabel := target.Tier
	if tierLabel == "" {
		tierLabel = "model"
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "unspecified"
	}
	log.Printf("[deep_think] escalate chat=%d tier=%s model=%s thinking=%s reason=%q request=%.80q",
		t.chatID, tierLabel, target.Model, target.Thinking, reason, request)

	prompt := deepThinkPrompt(request, userMessage)
	job := delegation.Job{
		Model:        target.Model,
		Thinking:     target.Thinking,
		MaxToolCalls: 20,
		MaxDuration:  10 * time.Minute,
		OutputFormat: delegation.OutputFormatMarkdown,
		OutputSchema: `Return the complete answer for the user: conclusions first, then the reasoning that supports them, then any caveats.`,
		MemoryPolicy: delegation.MemoryPolicyReadOnly,
		ToolAllowlist: []string{
			"web_fetch",
			"search",
			"memory_search",
			"memory_get",
			"obsidian",
			"grep",
			"search_file",
		},
	}.WithDefaults()

	result, err := t.submitter.SubmitAndWait(ctx, t.chatID, prompt, job)
	if err != nil {
		log.Printf("[deep_think] chat=%d tier=%s model=%s failed: %v", t.chatID, tierLabel, target.Model, err)
		return "", fmt.Errorf("deep_think failed: %w", err)
	}
	header := FormatDeepThinkHeader(tierLabel, target.Model, target.Thinking)
	return header + "\n\n" + result, nil
}

// FormatDeepThinkHeader renders the first line of a deep_think result.
func FormatDeepThinkHeader(tier, model, thinking string) string {
	return fmt.Sprintf("%s tier=%s model=%s thinking=%s", DeepThinkOutputPrefix, tier, model, thinking)
}

// ParseDeepThinkHeader reads the target back from a deep_think result. ok is
// false when the text does not start with the header.
func ParseDeepThinkHeader(output string) (tier, model, thinking string, ok bool) {
	first := output
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSpace(first)
	if !strings.HasPrefix(first, DeepThinkOutputPrefix) {
		return "", "", "", false
	}
	for _, field := range strings.Fields(strings.TrimPrefix(first, DeepThinkOutputPrefix)) {
		k, v, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		switch k {
		case "tier":
			tier = v
		case "model":
			model = v
		case "thinking":
			thinking = v
		}
	}
	return tier, model, thinking, model != ""
}

func deepThinkPrompt(request, userMessage string) string {
	var b strings.Builder
	b.WriteString(`You are the deep-reasoning worker for a chat assistant. The assistant escalated the request below because it needs careful, thorough thinking. Produce the answer the assistant will relay to the user.

REQUEST FROM THE ASSISTANT:
`)
	b.WriteString(strings.TrimSpace(request))
	if um := strings.TrimSpace(userMessage); um != "" {
		b.WriteString("\n\nTHE USER'S OWN MESSAGE (verbatim, for context):\n")
		b.WriteString(um)
	}
	b.WriteString(`

HOW YOU WORK:
- You have no access to the earlier conversation; everything you know is above. If something essential is missing, say what is missing and answer under stated assumptions.
- Reason step by step before concluding. Check your conclusions against the constraints in the request.
- Use the read-only tools only when a fact must be verified; do not browse for its own sake.
- Answer in the language of the user's message.
- Never send private code, credentials, or personal data to third-party services.

RETURN: the complete answer for the user — conclusions first, then the reasoning that supports them, then caveats. No preamble about being a worker.`)
	return b.String()
}

func (t *DeepThinkTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"request": map[string]interface{}{
				"type": "string",
				"description": "Self-contained request for the stronger model: the user's question verbatim plus every fact, " +
					"constraint, and prior conclusion it needs. The worker cannot see this conversation.",
			},
			"reason": map[string]interface{}{
				"type":        "string",
				"description": "Why this needs escalation, in one sentence (recorded in the run log).",
			},
			"tier": map[string]interface{}{
				"type":        "string",
				"description": "Optional cost tier to escalate to (e.g. premium). Empty selects the default tier. Mutually exclusive with model.",
			},
			"model": map[string]interface{}{
				"type":        "string",
				"description": "Optional explicit model id. Strong models are accepted only when the user's own message names them.",
			},
		},
		"required": []string{"request", "reason"},
	}
}
