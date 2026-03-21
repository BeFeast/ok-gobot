package cron

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "cron-test.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	return store
}

func TestScheduleCreatesJob(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	var executorCalled sync.WaitGroup
	executorCalled.Add(1)

	sched := NewScheduler(store, func(ctx context.Context, job storage.CronJob) (string, error) {
		defer executorCalled.Done()
		return "LLM result for: " + job.Task, nil
	})

	js := runtime.NewJobService(store)
	sched.SetJobService(js)

	var delivered struct {
		mu   sync.Mutex
		text string
	}
	sched.SetDeliverer(func(chatID int64, text string) {
		delivered.mu.Lock()
		delivered.text = text
		delivered.mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sched.Stop()

	// Schedule a job that fires every second.
	id, err := sched.AddJob("* * * * * *", "test task", 42)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero job ID")
	}

	// Wait for the executor to be invoked.
	done := make(chan struct{})
	go func() {
		executorCalled.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("executor was not called within 3s")
	}

	// Give the job runner time to finish persisting.
	time.Sleep(200 * time.Millisecond)

	// Verify a durable job was created.
	jobs, err := store.ListJobs(10)
	if err != nil {
		t.Fatalf("ListJobs failed: %v", err)
	}
	if len(jobs) == 0 {
		t.Fatal("expected at least one durable job")
	}
	if jobs[0].Kind != "cron" {
		t.Fatalf("job kind = %q, want cron", jobs[0].Kind)
	}

	// Verify report was delivered.
	delivered.mu.Lock()
	text := delivered.text
	delivered.mu.Unlock()
	if text == "" {
		t.Fatal("expected a report to be delivered")
	}
	if !strings.Contains(text, "Completed") {
		t.Fatalf("report missing success indicator: %s", text)
	}
}

func TestScheduleExecCreatesJob(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	sched := NewScheduler(store, func(ctx context.Context, job storage.CronJob) (string, error) {
		return "", nil
	})

	js := runtime.NewJobService(store)
	sched.SetJobService(js)

	var delivered struct {
		mu   sync.Mutex
		text string
	}
	sched.SetDeliverer(func(chatID int64, text string) {
		delivered.mu.Lock()
		delivered.text = text
		delivered.mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sched.Stop()

	id, err := sched.AddExecJob("* * * * * *", "echo hello-world", 100, 10)
	if err != nil {
		t.Fatalf("AddExecJob failed: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero job ID")
	}

	// Wait for the exec job to fire and persist.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err := store.ListJobs(10)
		if err != nil {
			t.Fatalf("ListJobs: %v", err)
		}
		for _, j := range jobs {
			if j.Status == string(runtime.JobStatusSucceeded) {
				delivered.mu.Lock()
				text := delivered.text
				delivered.mu.Unlock()
				if text == "" {
					t.Fatal("expected report delivery")
				}
				if !strings.Contains(text, "exec") {
					t.Fatalf("expected exec tag in report: %s", text)
				}
				if !strings.Contains(text, "hello-world") {
					t.Fatalf("expected command output in report: %s", text)
				}
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("exec job did not succeed within 3s")
}

func TestFormatReportSuccess(t *testing.T) {
	t.Parallel()

	cronJob := storage.CronJob{ID: 5, Task: "check status", Type: "llm"}
	result := runtime.JobRunResult{Summary: "All systems operational"}
	report := FormatReport(cronJob, "job-123", result, nil, 2*time.Minute+15*time.Second)

	if !strings.Contains(report, "Cron #5") {
		t.Fatalf("missing cron ID: %s", report)
	}
	if !strings.Contains(report, "Completed in 2m 15s") {
		t.Fatalf("missing duration: %s", report)
	}
	if !strings.Contains(report, "All systems operational") {
		t.Fatalf("missing summary: %s", report)
	}
	if !strings.Contains(report, "job-123") {
		t.Fatalf("missing job ID: %s", report)
	}
}

func TestFormatReportFailure(t *testing.T) {
	t.Parallel()

	cronJob := storage.CronJob{ID: 3, Task: "check status", Type: "exec"}
	result := runtime.JobRunResult{}
	report := FormatReport(cronJob, "job-456", result, errTest, 30*time.Second)

	if !strings.Contains(report, "Failed after 30s") {
		t.Fatalf("missing failure indicator: %s", report)
	}
	if !strings.Contains(report, "(exec)") {
		t.Fatalf("missing exec tag: %s", report)
	}
	if !strings.Contains(report, "test error") {
		t.Fatalf("missing error: %s", report)
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

var errTest error = testErr("test error")
