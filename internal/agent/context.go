package agent

import "ok-gobot/internal/ai"

// ContextMode controls how the message context is assembled before an AI call.
type ContextMode string

const (
	// ContextModeChat keeps a protected fresh tail of recent conversation
	// messages. Recency is king: the most recent messages survive trimming
	// even under heavy token pressure.
	ContextModeChat ContextMode = "chat"

	// ContextModeJob builds a task-focused context pack. Conversation history
	// is reduced to a small immediate-context window so the freed budget is
	// available for tool results and task output.
	ContextModeJob ContextMode = "job"
)

const (
	// chatTailProtected is the number of most-recent messages guaranteed to
	// survive trimming in chat mode, regardless of token budget.
	chatTailProtected = 10

	// chatHistoryBudget is the fraction of the model context window allocated
	// to conversation history in chat mode.
	chatHistoryBudget = 0.40

	// jobHistoryMessages is the maximum number of recent messages kept in job
	// mode — enough for immediate context, not enough to crowd out task work.
	jobHistoryMessages = 4
)

// AssembleContext builds the message slice for an AI call.
// It dispatches to the chat or job strategy based on mode.
func AssembleContext(
	mode ContextMode,
	systemPrompt string,
	history []ai.ChatMessage,
	userMsg ai.ChatMessage,
	model string,
) []ai.ChatMessage {
	switch mode {
	case ContextModeJob:
		return assembleJobContext(systemPrompt, history, userMsg)
	default:
		return assembleChatContext(systemPrompt, history, userMsg, model)
	}
}

// assembleChatContext keeps a protected fresh tail of recent messages.
// Older messages are trimmed token-aware from the front, but the last
// chatTailProtected messages are guaranteed to survive.
func assembleChatContext(
	systemPrompt string,
	history []ai.ChatMessage,
	userMsg ai.ChatMessage,
	model string,
) []ai.ChatMessage {
	msgs := make([]ai.ChatMessage, 0, 2+len(history))
	msgs = append(msgs, ai.ChatMessage{Role: ai.RoleSystem, Content: systemPrompt})

	if len(history) > 0 {
		history = TrimChatHistory(history, model)
		msgs = append(msgs, history...)
	}

	msgs = append(msgs, userMsg)
	return msgs
}

// TrimChatHistory trims history to fit the token budget while protecting
// the most recent chatTailProtected messages from eviction.
func TrimChatHistory(history []ai.ChatMessage, model string) []ai.ChatMessage {
	if len(history) == 0 {
		return history
	}

	budget := int(float64(ModelLimits(model)) * chatHistoryBudget)
	tc := NewTokenCounter()

	// Split into evictable prefix and protected tail.
	tailStart := len(history) - chatTailProtected
	if tailStart < 0 {
		tailStart = 0
	}

	tail := history[tailStart:]
	prefix := history[:tailStart]

	// Count tokens in the protected tail — these are non-negotiable.
	tailTokens := countChatTokens(tc, tail)
	if tailTokens >= budget {
		// Tail alone fills or exceeds budget; keep only the tail.
		return tail
	}

	// Budget remaining for the evictable prefix.
	remaining := budget - tailTokens
	prefix = trimPrefixToFit(tc, prefix, remaining)

	if len(prefix) == 0 {
		return tail
	}

	out := make([]ai.ChatMessage, 0, len(prefix)+len(tail))
	out = append(out, prefix...)
	out = append(out, tail...)
	return out
}

// assembleJobContext builds a task-focused context with minimal history.
// At most jobHistoryMessages recent messages are kept for immediate context;
// the rest of the token budget is reserved for tool results and task output.
func assembleJobContext(
	systemPrompt string,
	history []ai.ChatMessage,
	userMsg ai.ChatMessage,
) []ai.ChatMessage {
	msgs := make([]ai.ChatMessage, 0, 2+jobHistoryMessages)
	msgs = append(msgs, ai.ChatMessage{Role: ai.RoleSystem, Content: systemPrompt})

	if len(history) > jobHistoryMessages {
		history = history[len(history)-jobHistoryMessages:]
	}
	msgs = append(msgs, history...)

	msgs = append(msgs, userMsg)
	return msgs
}

// trimPrefixToFit drops messages from the front of msgs until total tokens
// fit within budget. Drops in pairs to maintain user/assistant alternation.
func trimPrefixToFit(tc *TokenCounter, msgs []ai.ChatMessage, budget int) []ai.ChatMessage {
	total := countChatTokens(tc, msgs)

	for len(msgs) > 1 && total > budget {
		total -= tc.CountTokens(msgs[0].Content) + 4
		msgs = msgs[1:]

		if len(msgs) > 0 {
			total -= tc.CountTokens(msgs[0].Content) + 4
			msgs = msgs[1:]
		}
	}

	if total > budget {
		return nil
	}

	return msgs
}

// countChatTokens estimates the total tokens for a slice of chat messages.
func countChatTokens(tc *TokenCounter, msgs []ai.ChatMessage) int {
	total := 0
	for _, m := range msgs {
		total += tc.CountTokens(m.Content) + 4 // +4 for message structure overhead
	}
	return total
}
