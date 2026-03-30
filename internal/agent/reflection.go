package agent

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// FailureThreshold is the number of repeated failures for the same tool
// before a fix is proposed in the memory log.
const FailureThreshold = 3

// toolFailure records a single tool execution failure.
type toolFailure struct {
	ToolName  string
	ErrMsg    string
	FirstSeen time.Time
	LastSeen  time.Time
	Count     int
}

// ReflectionTracker automatically analyzes tool failures and logs reflection
// entries to the daily memory note. After FailureThreshold repeated failures
// for the same tool it logs a fix-proposal entry. All writes are asynchronous
// so the main response flow is never blocked.
type ReflectionTracker struct {
	mu        sync.Mutex
	failures  map[string]*toolFailure
	memory    *Memory
	threshold int
}

// NewReflectionTracker creates a new tracker backed by the given Memory.
func NewReflectionTracker(mem *Memory) *ReflectionTracker {
	return &ReflectionTracker{
		failures:  make(map[string]*toolFailure),
		memory:    mem,
		threshold: FailureThreshold,
	}
}

// RecordFailure is called after a tool execution error. It updates in-memory
// state and asynchronously appends a reflection entry to the daily note.
// The call returns immediately and never blocks the caller.
func (r *ReflectionTracker) RecordFailure(toolName string, err error) {
	if err == nil {
		return
	}
	errMsg := err.Error()

	r.mu.Lock()
	rec, ok := r.failures[toolName]
	if !ok {
		rec = &toolFailure{
			ToolName:  toolName,
			ErrMsg:    errMsg,
			FirstSeen: time.Now(),
		}
		r.failures[toolName] = rec
	}
	rec.Count++
	rec.LastSeen = time.Now()
	// Capture threshold-crossing state before releasing lock.
	count := rec.Count
	r.mu.Unlock()

	go r.writeReflection(toolName, errMsg, count)
}

// writeReflection appends a markdown reflection entry to today's memory note.
// It is always called from a goroutine so it must not panic.
func (r *ReflectionTracker) writeReflection(toolName, errMsg string, count int) {
	var entry string
	if count >= r.threshold {
		entry = fmt.Sprintf(
			"**[reflection] tool `%s` failed %d times** — repeated error: %q\n"+
				"Suggested fix: review the skill configuration or implementation for `%s`; "+
				"consider updating SKILL.md with known failure patterns.",
			toolName, count, errMsg, toolName,
		)
		log.Printf("[reflection] tool %s reached failure threshold (%d) — fix proposed in daily note", toolName, count)
	} else {
		entry = fmt.Sprintf(
			"**[reflection] tool `%s` failed** (occurrence %d) — error: %q",
			toolName, count, errMsg,
		)
	}

	if err := r.memory.AppendToToday(entry); err != nil {
		log.Printf("[reflection] failed to write reflection for tool %s: %v", toolName, err)
	}
}

// FailureCount returns the current failure count for a tool (used in tests).
func (r *ReflectionTracker) FailureCount(toolName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if rec, ok := r.failures[toolName]; ok {
		return rec.Count
	}
	return 0
}

// Reset clears all recorded failures (used in tests).
func (r *ReflectionTracker) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = make(map[string]*toolFailure)
}
