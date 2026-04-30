package agent

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// FlushKind identifies a lifecycle moment that triggered a memory flush.
type FlushKind string

const (
	// FlushKindPreCompact runs before /compact (or any preflight compaction)
	// replaces transcript history with summaries.
	FlushKindPreCompact FlushKind = "pre-compact"
	// FlushKindJobSuccess runs after a successful long role/runtime job.
	FlushKindJobSuccess FlushKind = "job-success"
	// FlushKindJobFailure runs after a failed job to preserve diagnostics.
	FlushKindJobFailure FlushKind = "job-failure"
	// FlushKindJobTimeout runs after a timed-out job.
	FlushKindJobTimeout FlushKind = "job-timeout"
	// FlushKindJobCancelled runs after a cancelled job.
	FlushKindJobCancelled FlushKind = "job-cancelled"
)

// FlushRecord describes one bounded lifecycle memory flush opportunity. Fields
// are intentionally small: this is a guarded preservation hook, not a free-form
// memory write. Source IDs (JobID, SessionKey, RunID) are included for
// traceability so the daily-note draft can be tied back to its origin.
type FlushRecord struct {
	Kind       FlushKind
	JobID      string
	SessionKey string
	RunID      string
	RoleName   string
	// MessageCount is the transcript size at the time of a pre-compact flush.
	// Combined with SessionKey it forms the dedup key for that compaction.
	MessageCount int
	Summary      string
	Detail       string
	Artifacts    []string
}

// FlushResult is the outcome of one Flush call. A skipped flush is NOT an
// error: the caller (compaction, job lifecycle) should continue regardless so
// emergency compaction is never blocked indefinitely.
type FlushResult struct {
	Skipped  bool
	Reason   string
	Path     string
	DedupKey string
	Body     string
}

// ErrDurableMemoryNeedsApproval is returned when a caller asks the lifecycle
// flusher to silently mutate MEMORY.md. v1 forbids this: durable updates must
// be staged into a daily-note draft and then promoted by an admin.
var ErrDurableMemoryNeedsApproval = errors.New("durable MEMORY.md updates require explicit admin approval (v1)")

// LifecycleFlusher writes bounded, deduplicated memory drafts at session/job
// lifecycle moments. By default it appends a draft section to today's daily
// note via the underlying *Memory; it never silently rewrites MEMORY.md.
type LifecycleFlusher struct {
	mem *Memory
	now func() time.Time

	mu   sync.Mutex
	done map[string]bool
}

// NewLifecycleFlusher creates a flusher that writes drafts via mem. mem may be
// nil — in that case Flush returns a Skipped result rather than an error so a
// missing memory configuration never blocks compaction or job completion.
func NewLifecycleFlusher(mem *Memory) *LifecycleFlusher {
	return &LifecycleFlusher{
		mem:  mem,
		now:  time.Now,
		done: make(map[string]bool),
	}
}

// SetClock overrides the time source. Used by tests.
func (f *LifecycleFlusher) SetClock(now func() time.Time) {
	if f == nil || now == nil {
		return
	}
	f.now = now
}

// dedupKey returns the stable key used to suppress duplicate flushes for the
// same compaction or job state.
func dedupKey(rec FlushRecord) string {
	switch rec.Kind {
	case FlushKindPreCompact:
		return fmt.Sprintf("pre-compact|%s|%d", rec.SessionKey, rec.MessageCount)
	default:
		key := rec.JobID
		if key == "" {
			key = rec.SessionKey
		}
		return fmt.Sprintf("%s|%s", string(rec.Kind), key)
	}
}

// Flush writes a bounded lifecycle draft and returns a FlushResult. It never
// blocks the caller: a nil flusher, nil memory, or write error all surface as
// either an error result or a Skipped result, but the caller may continue.
//
// Dedup: repeat calls with the same (kind, source-id, message-count) are
// suppressed in-process so a retry of the same compaction or job state does
// not produce duplicate drafts.
func (f *LifecycleFlusher) Flush(rec FlushRecord) (FlushResult, error) {
	if f == nil {
		return FlushResult{Skipped: true, Reason: "lifecycle flusher not configured"}, nil
	}
	if rec.Kind == "" {
		return FlushResult{Skipped: true, Reason: "flush kind is empty"}, nil
	}

	key := dedupKey(rec)
	f.mu.Lock()
	if f.done[key] {
		f.mu.Unlock()
		return FlushResult{Skipped: true, Reason: "already flushed for this lifecycle state", DedupKey: key}, nil
	}
	// Reserve the key before writing so concurrent calls dedup even if the
	// write below fails. The clear-on-error path below releases the slot so
	// a real retry can succeed once the underlying problem is fixed.
	f.done[key] = true
	f.mu.Unlock()

	if f.mem == nil {
		return FlushResult{Skipped: true, Reason: "memory writer not configured", DedupKey: key}, nil
	}

	body := formatFlushBody(rec, f.clock())
	if err := f.mem.appendToTodayRaw("", body); err != nil {
		// Release the dedup slot so a follow-up retry can proceed once the
		// underlying write problem (disk full, perms) is resolved.
		f.mu.Lock()
		delete(f.done, key)
		f.mu.Unlock()
		return FlushResult{DedupKey: key, Body: body}, fmt.Errorf("lifecycle memory flush: %w", err)
	}

	note, _ := f.mem.GetTodayNote()
	path := ""
	if note != nil {
		path = note.Path
	}
	return FlushResult{DedupKey: key, Body: body, Path: path}, nil
}

// RequestDurableUpdate is the v1 guard for direct MEMORY.md writes. It always
// refuses: durable promotions must go through the admin-approval flow, not
// through automatic lifecycle flushes.
func (f *LifecycleFlusher) RequestDurableUpdate(rec FlushRecord) (FlushResult, error) {
	return FlushResult{
		Skipped:  true,
		Reason:   "durable memory write requires admin approval — staged as daily-note draft instead",
		DedupKey: dedupKey(rec),
	}, ErrDurableMemoryNeedsApproval
}

// Reset clears the in-process dedup table. Tests use this to re-exercise the
// same lifecycle moment without spinning up a fresh flusher.
func (f *LifecycleFlusher) Reset() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.done = make(map[string]bool)
	f.mu.Unlock()
}

func (f *LifecycleFlusher) clock() time.Time {
	if f.now == nil {
		return time.Now()
	}
	return f.now()
}

// formatFlushBody renders a draft section for today's daily note. Output is
// intentionally simple markdown: a header that flags this as a lifecycle
// draft (so a human reviewer can promote or discard), a kind tag, source IDs
// for traceability, and the bounded summary/detail.
func formatFlushBody(rec FlushRecord, ts time.Time) string {
	var sb strings.Builder
	sb.WriteString("\n\n## Lifecycle memory draft (")
	sb.WriteString(string(rec.Kind))
	sb.WriteString(" • ")
	sb.WriteString(ts.Format("15:04"))
	sb.WriteString(")\n")
	sb.WriteString("> draft pending review — promote to MEMORY.md only with admin approval\n")

	if rec.SessionKey != "" {
		fmt.Fprintf(&sb, "- session: `%s`\n", rec.SessionKey)
	}
	if rec.JobID != "" {
		fmt.Fprintf(&sb, "- job: `%s`\n", rec.JobID)
	}
	if rec.RunID != "" {
		fmt.Fprintf(&sb, "- run: `%s`\n", rec.RunID)
	}
	if rec.RoleName != "" {
		fmt.Fprintf(&sb, "- role: `%s`\n", rec.RoleName)
	}
	if rec.Kind == FlushKindPreCompact && rec.MessageCount > 0 {
		fmt.Fprintf(&sb, "- transcript size: %d messages\n", rec.MessageCount)
	}
	if rec.Summary != "" {
		fmt.Fprintf(&sb, "- summary: %s\n", strings.TrimSpace(rec.Summary))
	}
	if rec.Detail != "" {
		fmt.Fprintf(&sb, "- detail: %s\n", strings.TrimSpace(rec.Detail))
	}
	if len(rec.Artifacts) > 0 {
		sb.WriteString("- artifacts:\n")
		for _, a := range rec.Artifacts {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			fmt.Fprintf(&sb, "  - %s\n", a)
		}
	}

	return sb.String()
}
