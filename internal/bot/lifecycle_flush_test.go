package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

func newFlushFixture(t *testing.T) (*agent.LifecycleFlusher, string) {
	t.Helper()
	root := t.TempDir()
	mem := agent.NewMemory(root)
	flusher := agent.NewLifecycleFlusher(mem)
	flusher.SetClock(func() time.Time { return time.Date(2026, 4, 29, 11, 0, 0, 0, time.UTC) })
	return flusher, root
}

func readFlushDailyNote(t *testing.T, root string) string {
	t.Helper()
	date := time.Now().Format("2006-01-02")
	data, err := os.ReadFile(filepath.Join(root, "memory", date+".md"))
	if err != nil {
		t.Fatalf("read daily note: %v", err)
	}
	return string(data)
}

func TestRunLifecycleJobFlush_SuccessIncludesArtifacts(t *testing.T) {
	flusher, root := newFlushFixture(t)
	job := &storage.Job{JobID: "job-1", SessionKey: "chat:7"}
	result := runtime.JobRunResult{
		Summary: "scrape complete",
		Artifacts: []runtime.JobArtifactSpec{
			{Name: "report.md", URI: "file:///tmp/report.md"},
			{Name: "summary.txt"},
		},
	}

	runLifecycleJobFlush(context.Background(), flusher, job, "researcher", result, nil)

	body := readFlushDailyNote(t, root)
	if !strings.Contains(body, "job-success") {
		t.Fatalf("expected job-success header in:\n%s", body)
	}
	if !strings.Contains(body, "role: `researcher`") {
		t.Fatalf("missing role tag:\n%s", body)
	}
	if !strings.Contains(body, "report.md (file:///tmp/report.md)") {
		t.Fatalf("missing artifact entry:\n%s", body)
	}
	if !strings.Contains(body, "summary.txt") {
		t.Fatalf("missing un-URI artifact:\n%s", body)
	}
}

func TestRunLifecycleJobFlush_TimeoutKindFromContextDeadline(t *testing.T) {
	flusher, root := newFlushFixture(t)
	job := &storage.Job{JobID: "job-2"}

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)

	runLifecycleJobFlush(ctx, flusher, job, "researcher", runtime.JobRunResult{Summary: "partial"}, ctx.Err())

	body := readFlushDailyNote(t, root)
	if !strings.Contains(body, "job-timeout") {
		t.Fatalf("expected job-timeout kind in:\n%s", body)
	}
	if !strings.Contains(body, "context deadline exceeded") {
		t.Fatalf("expected timeout detail in:\n%s", body)
	}
}

func TestRunLifecycleJobFlush_CancelKind(t *testing.T) {
	flusher, root := newFlushFixture(t)
	job := &storage.Job{JobID: "job-3"}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runLifecycleJobFlush(ctx, flusher, job, "", runtime.JobRunResult{}, ctx.Err())

	body := readFlushDailyNote(t, root)
	if !strings.Contains(body, "job-cancelled") {
		t.Fatalf("expected job-cancelled kind in:\n%s", body)
	}
}

func TestRunLifecycleJobFlush_GenericFailure(t *testing.T) {
	flusher, root := newFlushFixture(t)
	job := &storage.Job{JobID: "job-4"}

	runLifecycleJobFlush(context.Background(), flusher, job, "", runtime.JobRunResult{}, errors.New("boom"))

	body := readFlushDailyNote(t, root)
	if !strings.Contains(body, "job-failure") {
		t.Fatalf("expected job-failure kind in:\n%s", body)
	}
	if !strings.Contains(body, "boom") {
		t.Fatalf("expected failure detail in:\n%s", body)
	}
}

func TestRunLifecycleJobFlush_DedupSameJobOutcome(t *testing.T) {
	flusher, root := newFlushFixture(t)
	job := &storage.Job{JobID: "job-5"}
	result := runtime.JobRunResult{Summary: "first"}

	runLifecycleJobFlush(context.Background(), flusher, job, "", result, nil)
	// Second call with the same job and same kind should be deduped.
	runLifecycleJobFlush(context.Background(), flusher, job, "", runtime.JobRunResult{Summary: "second"}, nil)

	body := readFlushDailyNote(t, root)
	if strings.Count(body, "Lifecycle memory draft (job-success") != 1 {
		t.Fatalf("expected exactly one job-success draft, got:\n%s", body)
	}
	if strings.Contains(body, "second") {
		t.Fatalf("dedup leaked second flush content into note:\n%s", body)
	}
}

func TestRunLifecycleJobFlush_NilFlusherIsSafe(t *testing.T) {
	// Must not panic when the bot has no lifecycle flusher (e.g. tests
	// that build a partial *Bot without wiring memory).
	runLifecycleJobFlush(context.Background(), nil, &storage.Job{JobID: "job-6"}, "", runtime.JobRunResult{}, nil)
}

func TestRunLifecycleJobFlush_NilJobIsSafe(t *testing.T) {
	flusher, _ := newFlushFixture(t)
	runLifecycleJobFlush(context.Background(), flusher, nil, "", runtime.JobRunResult{}, nil)
}
