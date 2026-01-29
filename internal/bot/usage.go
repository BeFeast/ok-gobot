package bot

import (
	"fmt"
	"sync"
)

// UsageTracker tracks per-chat token usage for the current request
type UsageTracker struct {
	mu    sync.Mutex
	chats map[int64]*RequestUsage
}

// RequestUsage holds usage data for a single request cycle
type RequestUsage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// NewUsageTracker creates a new usage tracker
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{
		chats: make(map[int64]*RequestUsage),
	}
}

// Record records token usage for a chat
func (u *UsageTracker) Record(chatID int64, prompt, completion, total int) {
	u.mu.Lock()
	defer u.mu.Unlock()

	existing, ok := u.chats[chatID]
	if !ok {
		u.chats[chatID] = &RequestUsage{
			PromptTokens:     prompt,
			CompletionTokens: completion,
			TotalTokens:      total,
		}
		return
	}
	// Accumulate across multiple API calls (tool calling iterations)
	existing.PromptTokens = prompt // Last prompt size is the current context size
	existing.CompletionTokens += completion
	existing.TotalTokens = total
}

// Consume returns and clears usage for a chat
func (u *UsageTracker) Consume(chatID int64) *RequestUsage {
	u.mu.Lock()
	defer u.mu.Unlock()

	usage, ok := u.chats[chatID]
	if !ok {
		return nil
	}
	delete(u.chats, chatID)
	return usage
}

// FormatUsageFooter formats token usage as a footer string
func FormatUsageFooter(prompt, completion int) string {
	return fmt.Sprintf("📊 %s in / %s out", formatTokenCount(prompt), formatTokenCount(completion))
}

func formatTokenCount(tokens int) string {
	if tokens >= 1000 {
		return fmt.Sprintf("%.1fk", float64(tokens)/1000)
	}
	return fmt.Sprintf("%d", tokens)
}
