package runtime

import (
	"sync"
	"time"
)

// RouteRecord captures a single chat-router decision for observability.
type RouteRecord struct {
	Timestamp  time.Time  `json:"timestamp"`
	SessionKey string     `json:"session_key"`
	Input      string     `json:"input"`
	Action     ChatAction `json:"action"`
	Reason     string     `json:"reason"`
}

// RouteLog is a bounded, thread-safe ring buffer of recent route decisions.
type RouteLog struct {
	mu      sync.Mutex
	entries []RouteRecord
	maxSize int
}

// NewRouteLog creates a RouteLog that keeps the most recent maxSize entries.
func NewRouteLog(maxSize int) *RouteLog {
	if maxSize <= 0 {
		maxSize = 200
	}
	return &RouteLog{
		entries: make([]RouteRecord, 0, maxSize),
		maxSize: maxSize,
	}
}

// Record appends a route decision to the log, evicting the oldest entry
// when at capacity.
func (l *RouteLog) Record(sessionKey, input string, decision ChatRouteDecision) {
	l.mu.Lock()
	defer l.mu.Unlock()

	rec := RouteRecord{
		Timestamp:  time.Now(),
		SessionKey: sessionKey,
		Input:      truncate(input, 512),
		Action:     decision.Action,
		Reason:     decision.Reason,
	}

	if len(l.entries) >= l.maxSize {
		// Shift left by 1.
		copy(l.entries, l.entries[1:])
		l.entries[len(l.entries)-1] = rec
	} else {
		l.entries = append(l.entries, rec)
	}
}

// Recent returns up to limit recent entries, newest first.
func (l *RouteLog) Recent(limit int) []RouteRecord {
	l.mu.Lock()
	defer l.mu.Unlock()

	if limit <= 0 || limit > len(l.entries) {
		limit = len(l.entries)
	}

	out := make([]RouteRecord, limit)
	for i := 0; i < limit; i++ {
		out[i] = l.entries[len(l.entries)-1-i]
	}
	return out
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
