package agent

import (
	"context"
	"fmt"
	"strings"

	"ok-gobot/internal/ai"
)

// Compactor handles context compaction by summarizing old messages
type Compactor struct {
	aiClient     ai.Client
	tokenCounter *TokenCounter
	threshold    float64 // Percentage of context to trigger compaction (0.8 = 80%)
	model        string
}

// NewCompactor creates a new context compactor
func NewCompactor(aiClient ai.Client, model string) *Compactor {
	return &Compactor{
		aiClient:     aiClient,
		tokenCounter: NewTokenCounter(),
		threshold:    0.8, // Compact at 80% of context limit
		model:        model,
	}
}

// SetThreshold sets the compaction threshold (0.0 to 1.0)
func (c *Compactor) SetThreshold(threshold float64) {
	if threshold > 0 && threshold <= 1.0 {
		c.threshold = threshold
	}
}

// ShouldCompact determines if context should be compacted
func (c *Compactor) ShouldCompact(messages []ai.Message) bool {
	msgs := make([]Message, len(messages))
	for i, m := range messages {
		msgs[i] = Message{Role: m.Role, Content: m.Content}
	}
	return c.tokenCounter.ShouldCompact(msgs, c.model, c.threshold)
}

// ShouldCompactByTokens determines if context should be compacted based on token count
func (c *Compactor) ShouldCompactByTokens(currentTokens int) bool {
	limit := ModelLimits(c.model)
	maxTokens := int(float64(limit) * c.threshold)
	return currentTokens > maxTokens
}

// Compact summarizes a conversation into a condensed format
func (c *Compactor) Compact(ctx context.Context, messages []ai.Message) (*CompactionResult, error) {
	if c.aiClient == nil {
		return nil, fmt.Errorf("AI client not configured")
	}

	// Build compaction prompt
	prompt := `You are a context compaction assistant. Your task is to summarize the following conversation into a condensed format that preserves all important information, decisions, and context.

Rules:
1. Keep all specific facts, numbers, dates, and names
2. Preserve all decisions and conclusions
3. Maintain the flow of the conversation
4. Remove redundant or repetitive content
5. Format as a brief summary followed by key points

Conversation to summarize:`

	// Add messages to summarize (skip system prompt)
	var conversation strings.Builder
	for _, msg := range messages {
		if msg.Role != "system" {
			conversation.WriteString(fmt.Sprintf("\n%s: %s\n", msg.Role, msg.Content))
		}
	}

	// Call AI to summarize
	summaryMessages := []ai.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: conversation.String()},
	}

	summary, err := c.aiClient.Complete(ctx, summaryMessages)
	if err != nil {
		return nil, fmt.Errorf("failed to generate summary: %w", err)
	}

	// Calculate savings
	originalTokens := estimateTokens(conversation.String())
	summaryTokens := estimateTokens(summary)
	savings := originalTokens - summaryTokens

	return &CompactionResult{
		Summary:        summary,
		OriginalTokens: originalTokens,
		SummaryTokens:  summaryTokens,
		TokensSaved:    savings,
	}, nil
}

// CompactionResult holds the results of compaction
type CompactionResult struct {
	Summary        string
	OriginalTokens int
	SummaryTokens  int
	TokensSaved    int
}

// FormatNotification formats the compaction result for display
func (r *CompactionResult) FormatNotification() string {
	return fmt.Sprintf("🗜️ Context compacted\nTokens saved: %d → %d (%d saved)",
		r.OriginalTokens, r.SummaryTokens, r.TokensSaved)
}

// estimateTokens roughly estimates token count
func estimateTokens(text string) int {
	// Rough estimate: ~4 characters per token
	return len(text) / 4
}

// CompactSession compacts a session and returns the new session content
func (c *Compactor) CompactSession(ctx context.Context, currentSession string) (string, error) {
	if currentSession == "" {
		return "", nil
	}

	// Create a single message from the session
	messages := []ai.Message{
		{Role: "assistant", Content: currentSession},
	}

	result, err := c.Compact(ctx, messages)
	if err != nil {
		return "", err
	}

	return result.Summary, nil
}
