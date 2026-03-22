package cron

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

func TestFireCronJobCreatesRuntimeJob(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	svc := runtime.NewJobService(store)

	var delivered []JobReport
	var mu sync.Mutex

	sched := NewScheduler(store, func(ctx context.Context, job storage.CronJob) error {
		return nil // LLM executor succeeds
	})
	sched.SetJobService(svc)
	sched.SetDeliverer(func(chatID int64, report JobReport) {
		mu.Lock()
		delivered = append(delivered, report)
		mu.Unlock()
	})

	cronJob := storage.CronJob{
		ID:     1,
		Task:   "summarize news",
		ChatID: 42,
		Type:   "llm",
	}

	sched.fireCronJob(cronJob, 5*time.Second)

	// Wait for the async job to complete.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(delivered)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(delivered) != 1 {
		t.Fatalf("expected 1 report, got %d", len(delivered))
	}

	r := delivered[0]
	if r.CronJobID != 1 {
		t.Errorf("CronJobID = %d, want 1", r.CronJobID)
	}
	if r.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", r.Status)
	}
	if r.Type != "llm" {
		t.Errorf("Type = %q, want llm", r.Type)
	}
	if r.JobID == "" {
		t.Error("expected non-empty runtime JobID")
	}
}

func TestFireCronJobExecCreatesRuntimeJob(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	svc := runtime.NewJobService(store)

	var delivered []JobReport
	var mu sync.Mutex

	sched := NewScheduler(store, nil)
	sched.SetJobService(svc)
	sched.SetDeliverer(func(chatID int64, report JobReport) {
		mu.Lock()
		delivered = append(delivered, report)
		mu.Unlock()
	})

	cronJob := storage.CronJob{
		ID:     2,
		Task:   "echo hello",
		ChatID: 99,
		Type:   "exec",
	}

	sched.fireCronJob(cronJob, 5*time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(delivered)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(delivered) != 1 {
		t.Fatalf("expected 1 report, got %d", len(delivered))
	}

	r := delivered[0]
	if r.CronJobID != 2 {
		t.Errorf("CronJobID = %d, want 2", r.CronJobID)
	}
	if r.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", r.Status)
	}
	if r.Type != "exec" {
		t.Errorf("Type = %q, want exec", r.Type)
	}
	if r.Output != "hello" {
		t.Errorf("Output = %q, want %q", r.Output, "hello")
	}
}

func TestFireCronJobFallsBackWithoutJobService(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	var executorCalled bool
	sched := NewScheduler(store, func(ctx context.Context, job storage.CronJob) error {
		executorCalled = true
		return nil
	})
	// No SetJobService — should use legacy path.

	cronJob := storage.CronJob{
		ID:     3,
		Task:   "check status",
		ChatID: 7,
		Type:   "llm",
	}

	sched.fireCronJob(cronJob, 5*time.Second)
	if !executorCalled {
		t.Error("expected legacy executor to be called when no JobService is set")
	}
}

func TestReportFormatTelegram(t *testing.T) {
	t.Parallel()

	r := JobReport{
		CronJobID: 5,
		JobID:     "job-123",
		Type:      "exec",
		Task:      "uptime",
		Status:    "succeeded",
		Summary:   "command completed",
		Output:    "up 42 days",
		Duration:  1234 * time.Millisecond,
	}

	msg := r.FormatTelegram()

	for _, want := range []string{"#5", "succeeded", "job-123", "exec", "1.234s", "up 42 days"} {
		if !contains(msg, want) {
			t.Errorf("report missing %q:\n%s", want, msg)
		}
	}
}

func TestReportFormatTelegramFailed(t *testing.T) {
	t.Parallel()

	r := JobReport{
		CronJobID: 6,
		Type:      "llm",
		Status:    "failed",
		Error:     "context deadline exceeded",
	}

	msg := r.FormatTelegram()

	if !contains(msg, "❌") {
		t.Error("expected failure emoji in report")
	}
	if !contains(msg, "context deadline exceeded") {
		t.Error("expected error message in report")
	}
}

func TestReportFormatTelegramTruncation(t *testing.T) {
	t.Parallel()

	longOutput := ""
	for i := 0; i < 5000; i++ {
		longOutput += "x"
	}

	r := JobReport{
		CronJobID: 7,
		Type:      "exec",
		Status:    "succeeded",
		Output:    longOutput,
	}

	msg := r.FormatTelegram()
	if len(msg) > 4100 {
		t.Errorf("report too long: %d chars", len(msg))
	}
	if !contains(msg, "(truncated)") {
		t.Error("expected truncation marker")
	}
}

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()

	store, err := storage.New(filepath.Join(t.TempDir(), "cron-test.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	return store
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsImpl(s, substr))
}

func containsImpl(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
