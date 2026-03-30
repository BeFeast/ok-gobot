package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"ok-gobot/internal/logger"
)

const defaultFailureThreshold = 3

// FailureRecord captures a single tool execution failure for later analysis.
type FailureRecord struct {
	ToolName     string
	Args         string
	ErrMsg       string
	OccurredAt   time.Time
	SuggestedFix string
}

// Reflector analyzes tool failures and proposes improvements.
// All methods are safe for concurrent use.
type Reflector struct {
	mu            sync.Mutex
	failureCounts map[string]int
	recentErrors  map[string][]FailureRecord
	threshold     int
	memory        *Memory // optional; when set, failures are appended to today's note
}

// NewReflector creates a Reflector that triggers after threshold repeated failures.
// threshold <= 0 defaults to 3.
func NewReflector(threshold int, memory *Memory) *Reflector {
	if threshold <= 0 {
		threshold = defaultFailureThreshold
	}
	return &Reflector{
		failureCounts: make(map[string]int),
		recentErrors:  make(map[string][]FailureRecord),
		threshold:     threshold,
		memory:        memory,
	}
}

// RecordFailureAsync enqueues a failure analysis in a background goroutine
// so it never blocks the main response flow.
func (r *Reflector) RecordFailureAsync(toolName, args string, err error) {
	go r.recordFailure(toolName, args, err)
}

func (r *Reflector) recordFailure(toolName, args string, toolErr error) {
	errMsg := ""
	if toolErr != nil {
		errMsg = toolErr.Error()
	}

	suggestion := deriveSuggestedFix(toolName, errMsg)

	record := FailureRecord{
		ToolName:     toolName,
		Args:         args,
		ErrMsg:       errMsg,
		OccurredAt:   time.Now(),
		SuggestedFix: suggestion,
	}

	r.mu.Lock()
	r.failureCounts[toolName]++
	count := r.failureCounts[toolName]
	r.recentErrors[toolName] = append(r.recentErrors[toolName], record)
	// Keep at most 10 recent records per tool to bound memory usage.
	if len(r.recentErrors[toolName]) > 10 {
		r.recentErrors[toolName] = r.recentErrors[toolName][len(r.recentErrors[toolName])-10:]
	}
	r.mu.Unlock()

	logger.Debugf("[reflection] tool=%s failure=%d/%d err=%q", toolName, count, r.threshold, errMsg)

	if count >= r.threshold {
		r.proposeOrApplyFix(toolName, count, record)
	}

	if r.memory != nil {
		entry := fmt.Sprintf("Tool failure [%s] #%d: %s\n  Suggested fix: %s", toolName, count, errMsg, suggestion)
		if appendErr := r.memory.AppendToToday(entry); appendErr != nil {
			logger.Debugf("[reflection] failed to persist failure record: %v", appendErr)
		}
	}
}

// proposeOrApplyFix is called once failure count reaches the threshold.
// Currently it logs a structured proposal; a future implementation could
// send a patch to the tool's SKILL.md via the AI client.
func (r *Reflector) proposeOrApplyFix(toolName string, count int, record FailureRecord) {
	logger.Warnf(
		"[reflection] tool %q has failed %d times — proposed fix: %s",
		toolName, count, record.SuggestedFix,
	)
}

// FailureCount returns the number of recorded failures for the given tool.
func (r *Reflector) FailureCount(toolName string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failureCounts[toolName]
}

// RecentErrors returns a snapshot of the most recent failure records for toolName.
func (r *Reflector) RecentErrors(toolName string) []FailureRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.recentErrors[toolName]
	if len(src) == 0 {
		return nil
	}
	out := make([]FailureRecord, len(src))
	copy(out, src)
	return out
}

// deriveSuggestedFix produces a human-readable suggestion based on common
// error patterns. It is intentionally heuristic and conservative.
func deriveSuggestedFix(toolName, errMsg string) string {
	lower := strings.ToLower(errMsg)
	switch {
	case strings.Contains(lower, "not found") || strings.Contains(lower, "no such"):
		return fmt.Sprintf("verify that %q is registered and its dependencies are available", toolName)
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline"):
		return fmt.Sprintf("consider increasing the timeout or reducing the scope of %q inputs", toolName)
	case strings.Contains(lower, "permission") || strings.Contains(lower, "denied"):
		return fmt.Sprintf("check that the runtime has the required permissions for %q", toolName)
	case strings.Contains(lower, "parse") || strings.Contains(lower, "unmarshal") || strings.Contains(lower, "invalid"):
		return fmt.Sprintf("review the input schema for %q — arguments may be malformed", toolName)
	case strings.Contains(lower, "connect") || strings.Contains(lower, "dial") || strings.Contains(lower, "network"):
		return fmt.Sprintf("check network connectivity required by %q", toolName)
	default:
		return fmt.Sprintf("investigate recurring error in %q: %q", toolName, errMsg)
	}
}
