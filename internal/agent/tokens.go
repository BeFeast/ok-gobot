package agent

import (
	"strings"
	"sync"
	"unicode/utf8"
)

// TokenCounter estimates token counts for text
type TokenCounter struct {
	mu sync.RWMutex
}

// NewTokenCounter creates a new token counter
func NewTokenCounter() *TokenCounter {
	return &TokenCounter{}
}

// CountTokens estimates the number of tokens in text
// This is a simple approximation - for exact counts, use tiktoken-go
func (tc *TokenCounter) CountTokens(text string) int {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	// Simple approximation: ~4 chars per token for English
	// For more accurate counts, integrate tiktoken-go
	charCount := utf8.RuneCountInString(text)

	// Count words as a secondary metric
	words := len(strings.Fields(text))

	// Use weighted average of char-based and word-based estimates
	charBasedEstimate := charCount / 4
	wordBasedEstimate := int(float64(words) * 1.3) // ~1.3 tokens per word

	// Return the higher estimate to be conservative
	if charBasedEstimate > wordBasedEstimate {
		return charBasedEstimate
	}
	return wordBasedEstimate
}

// CountMessages estimates tokens for a slice of messages
func (tc *TokenCounter) CountMessages(messages []Message) int {
	total := 0
	for _, msg := range messages {
		// Add overhead for message structure (~4 tokens per message)
		total += 4
		total += tc.CountTokens(msg.Content)
	}
	return total
}

// Message represents a chat message for token counting
type Message struct {
	Role    string
	Content string
}

// ModelLimits returns the context window size for common models
func ModelLimits(model string) int {
	limits := map[string]int{
		// OpenAI
		"gpt-4o":          128000,
		"gpt-4o-mini":     128000,
		"gpt-4-turbo":     128000,
		"gpt-4":           8192,
		"gpt-3.5-turbo":   16385,

		// Anthropic (via OpenRouter)
		"anthropic/claude-3.5-sonnet": 200000,
		"anthropic/claude-3-opus":     200000,
		"anthropic/claude-3-sonnet":   200000,

		// Google
		"google/gemini-pro-1.5": 1000000,

		// Meta
		"meta-llama/llama-3.1-70b": 128000,
		"meta-llama/llama-3.1-8b":  128000,

		// Kimi
		"moonshotai/kimi-k2.5": 131072,

		// Default for unknown models
		"default": 8192,
	}

	if limit, ok := limits[model]; ok {
		return limit
	}

	// Try partial match
	for name, limit := range limits {
		if strings.Contains(model, name) {
			return limit
		}
	}

	return limits["default"]
}

// ShouldCompact returns true if the messages exceed the threshold
func (tc *TokenCounter) ShouldCompact(messages []Message, model string, threshold float64) bool {
	if threshold <= 0 || threshold > 1 {
		threshold = 0.8 // Default to 80% of context
	}

	tokenCount := tc.CountMessages(messages)
	limit := ModelLimits(model)
	maxTokens := int(float64(limit) * threshold)

	return tokenCount > maxTokens
}
