package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestFlusher(t *testing.T) (*LifecycleFlusher, string) {
	t.Helper()
	root := t.TempDir()
	mem := NewMemory(root)
	f := NewLifecycleFlusher(mem)
	f.SetClock(func() time.Time { return time.Date(2026, 4, 29, 10, 30, 0, 0, time.UTC) })
	return f, root
}

func todayNotePath(root string) string {
	date := time.Now().Format("2006-01-02")
	return filepath.Join(root, "memory", date+".md")
}

func readTodayNote(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(todayNotePath(root))
	if err != nil {
		t.Fatalf("read today note: %v", err)
	}
	return string(data)
}

func TestLifecycleFlush_PreCompactWritesDraft(t *testing.T) {
	f, root := newTestFlusher(t)

	res, err := f.Flush(FlushRecord{
		Kind:         FlushKindPreCompact,
		SessionKey:   "chat:42",
		MessageCount: 120,
		Summary:      "open todos: ship feature X, retire old auth",
	})
	if err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if res.Skipped {
		t.Fatalf("expected write, got skip: %s", res.Reason)
	}
	if res.Path == "" || res.Path != todayNotePath(root) {
		t.Fatalf("unexpected path: %q", res.Path)
	}

	body := readTodayNote(t, root)
	if !strings.Contains(body, "Lifecycle memory draft (pre-compact") {
		t.Fatalf("daily note missing pre-compact header:\n%s", body)
	}
	if !strings.Contains(body, "session: `chat:42`") {
		t.Fatalf("daily note missing session id:\n%s", body)
	}
	if !strings.Contains(body, "transcript size: 120 messages") {
		t.Fatalf("daily note missing message count:\n%s", body)
	}
	if !strings.Contains(body, "draft pending review") {
		t.Fatalf("draft missing review-required marker:\n%s", body)
	}
}

func TestLifecycleFlush_JobSuccessIncludesArtifacts(t *testing.T) {
	f, root := newTestFlusher(t)

	res, err := f.Flush(FlushRecord{
		Kind:      FlushKindJobSuccess,
		JobID:     "job-1",
		RoleName:  "researcher",
		Summary:   "compiled latest pipeline metrics",
		Artifacts: []string{"reports/2026-04/metrics.md", "reports/2026-04/plot.png"},
	})
	if err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if res.Skipped {
		t.Fatalf("expected write, got skip: %s", res.Reason)
	}

	body := readTodayNote(t, root)
	if !strings.Contains(body, "Lifecycle memory draft (job-success") {
		t.Fatalf("missing job-success header:\n%s", body)
	}
	if !strings.Contains(body, "job: `job-1`") {
		t.Fatalf("missing job id:\n%s", body)
	}
	if !strings.Contains(body, "role: `researcher`") {
		t.Fatalf("missing role name:\n%s", body)
	}
	if !strings.Contains(body, "reports/2026-04/metrics.md") {
		t.Fatalf("missing artifact path:\n%s", body)
	}
}

func TestLifecycleFlush_TimeoutAndFailurePreserveDetail(t *testing.T) {
	cases := []struct {
		name string
		kind FlushKind
		want string
	}{
		{"timeout", FlushKindJobTimeout, "job-timeout"},
		{"failure", FlushKindJobFailure, "job-failure"},
		{"cancelled", FlushKindJobCancelled, "job-cancelled"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, root := newTestFlusher(t)
			res, err := f.Flush(FlushRecord{
				Kind:    tc.kind,
				JobID:   "job-x",
				Summary: "background scrape",
				Detail:  "context deadline exceeded after 5m",
			})
			if err != nil {
				t.Fatalf("Flush() error = %v", err)
			}
			if res.Skipped {
				t.Fatalf("expected write, got skip: %s", res.Reason)
			}
			body := readTodayNote(t, root)
			if !strings.Contains(body, tc.want) {
				t.Fatalf("missing kind tag %q in:\n%s", tc.want, body)
			}
			if !strings.Contains(body, "context deadline exceeded after 5m") {
				t.Fatalf("missing diagnostic detail in:\n%s", body)
			}
		})
	}
}

func TestLifecycleFlush_DeduplicatesSameState(t *testing.T) {
	f, root := newTestFlusher(t)

	rec := FlushRecord{
		Kind:         FlushKindPreCompact,
		SessionKey:   "chat:99",
		MessageCount: 50,
		Summary:      "first call",
	}
	first, err := f.Flush(rec)
	if err != nil {
		t.Fatalf("first Flush() error = %v", err)
	}
	if first.Skipped {
		t.Fatalf("first call unexpectedly skipped: %s", first.Reason)
	}

	rec.Summary = "second call (should be ignored)"
	second, err := f.Flush(rec)
	if err != nil {
		t.Fatalf("second Flush() error = %v", err)
	}
	if !second.Skipped {
		t.Fatalf("expected duplicate flush to skip, got body=%q", second.Body)
	}
	if second.DedupKey != first.DedupKey {
		t.Fatalf("dedup keys diverged: first=%q second=%q", first.DedupKey, second.DedupKey)
	}

	body := readTodayNote(t, root)
	if strings.Count(body, "Lifecycle memory draft (pre-compact") != 1 {
		t.Fatalf("expected exactly one draft section, got:\n%s", body)
	}
	if strings.Contains(body, "second call (should be ignored)") {
		t.Fatalf("second flush leaked into note:\n%s", body)
	}

	// A different message count is a different compaction state — should write.
	third, err := f.Flush(FlushRecord{
		Kind:         FlushKindPreCompact,
		SessionKey:   "chat:99",
		MessageCount: 80,
		Summary:      "later compaction",
	})
	if err != nil {
		t.Fatalf("third Flush() error = %v", err)
	}
	if third.Skipped {
		t.Fatalf("expected new state to write, got skip: %s", third.Reason)
	}
}

func TestLifecycleFlush_DedupAcrossJobKinds(t *testing.T) {
	f, _ := newTestFlusher(t)

	if _, err := f.Flush(FlushRecord{Kind: FlushKindJobSuccess, JobID: "job-7"}); err != nil {
		t.Fatalf("success flush error = %v", err)
	}
	// A failure for the same job should still be allowed: the kind differs,
	// so the lifecycle state is distinct.
	res, err := f.Flush(FlushRecord{Kind: FlushKindJobFailure, JobID: "job-7"})
	if err != nil {
		t.Fatalf("failure flush error = %v", err)
	}
	if res.Skipped {
		t.Fatalf("expected failure flush to write despite earlier success, got skip: %s", res.Reason)
	}
}

func TestLifecycleFlush_DurableMemoryRefusedInV1(t *testing.T) {
	f, _ := newTestFlusher(t)

	res, err := f.RequestDurableUpdate(FlushRecord{
		Kind:    FlushKindJobSuccess,
		JobID:   "job-9",
		Summary: "durable note candidate",
	})
	if !errors.Is(err, ErrDurableMemoryNeedsApproval) {
		t.Fatalf("expected ErrDurableMemoryNeedsApproval, got %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected skipped result, got %#v", res)
	}
	if !strings.Contains(res.Reason, "admin approval") {
		t.Fatalf("reason should mention admin approval: %q", res.Reason)
	}
}

func TestLifecycleFlush_NilFlusherSafe(t *testing.T) {
	var f *LifecycleFlusher
	res, err := f.Flush(FlushRecord{Kind: FlushKindJobSuccess, JobID: "job-1"})
	if err != nil {
		t.Fatalf("nil flusher Flush() error = %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected skip on nil flusher, got %#v", res)
	}
}

func TestLifecycleFlush_NilMemorySkipsWithoutError(t *testing.T) {
	f := NewLifecycleFlusher(nil)
	f.SetClock(func() time.Time { return time.Date(2026, 4, 29, 10, 30, 0, 0, time.UTC) })
	res, err := f.Flush(FlushRecord{Kind: FlushKindJobSuccess, JobID: "job-1"})
	if err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	if !res.Skipped {
		t.Fatalf("expected skip when memory is nil, got %#v", res)
	}
	if res.Reason == "" {
		t.Fatalf("expected reason explaining skip")
	}
}

func TestLifecycleFlush_WriteFailureClearsDedupForRetry(t *testing.T) {
	root := t.TempDir()
	mem := NewMemory(root)

	// Pre-create a directory at the today-note path so the file write fails
	// (open returns EISDIR or similar). This simulates a transient I/O issue.
	memDir := filepath.Join(root, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	date := time.Now().Format("2006-01-02")
	conflict := filepath.Join(memDir, date+".md")
	if err := os.MkdirAll(conflict, 0o755); err != nil {
		t.Fatalf("create conflicting dir: %v", err)
	}

	f := NewLifecycleFlusher(mem)
	f.SetClock(func() time.Time { return time.Date(2026, 4, 29, 10, 30, 0, 0, time.UTC) })

	rec := FlushRecord{Kind: FlushKindJobSuccess, JobID: "job-z"}
	if _, err := f.Flush(rec); err == nil {
		t.Fatalf("expected write error from path conflict, got nil")
	}

	// Replace the conflicting directory with a writable file location so the
	// retry can succeed; flush must not be permanently dedup-blocked.
	if err := os.RemoveAll(conflict); err != nil {
		t.Fatalf("remove conflict: %v", err)
	}

	res, err := f.Flush(rec)
	if err != nil {
		t.Fatalf("retry Flush() error = %v", err)
	}
	if res.Skipped {
		t.Fatalf("retry unexpectedly skipped: %s", res.Reason)
	}
}
