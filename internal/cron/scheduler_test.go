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

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "cron-test.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	return store
}

func TestSchedulerDurableExecJob(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	jobService := runtime.NewJobService(store)

	var mu sync.Mutex
	var delivered []JobReport

	sched := NewScheduler(store, nil)
	sched.SetJobService(jobService)
	sched.SetReportDeliverer(func(chatID int64, report JobReport) {
		mu.Lock()
		delivered = append(delivered, report)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sched.Stop()

	// Schedule an exec job that fires every second
	cronID, err := sched.AddExecJob("* * * * * *", "echo hello-durable", 42, 10)
	if err != nil {
		t.Fatalf("AddExecJob failed: %v", err)
	}

	// Wait for at least one report delivery
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(delivered)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	mu.Lock()
	reports := delivered
	mu.Unlock()

	if len(reports) == 0 {
		t.Fatal("expected at least one report delivery")
	}

	r := reports[0]
	if r.CronJobID != cronID {
		t.Errorf("CronJobID = %d, want %d", r.CronJobID, cronID)
	}
	if r.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", r.Status)
	}
	if r.JobID == "" {
		t.Error("expected durable JobID to be set")
	}
	if r.JobType != "exec" {
		t.Errorf("JobType = %q, want exec", r.JobType)
	}

	// Verify a durable job was persisted
	j, err := store.GetJob(r.JobID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if j == nil {
		t.Fatal("expected durable job to exist in storage")
	}
	if j.Kind != "cron_exec" {
		t.Errorf("job kind = %q, want cron_exec", j.Kind)
	}
	if j.Status != "succeeded" {
		t.Errorf("job status = %q, want succeeded", j.Status)
	}
}

func TestSchedulerDurableLLMJob(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	jobService := runtime.NewJobService(store)

	var executedMu sync.Mutex
	var executedTasks []string

	var reportMu sync.Mutex
	var delivered []JobReport

	executor := func(ctx context.Context, job storage.CronJob) error {
		executedMu.Lock()
		executedTasks = append(executedTasks, job.Task)
		executedMu.Unlock()
		return nil
	}

	sched := NewScheduler(store, executor)
	sched.SetJobService(jobService)
	sched.SetReportDeliverer(func(chatID int64, report JobReport) {
		reportMu.Lock()
		delivered = append(delivered, report)
		reportMu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sched.Stop()

	// Schedule an LLM job that fires every second
	cronID, err := sched.AddJob("* * * * * *", "summarize today", 100)
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Wait for at least one report delivery
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		reportMu.Lock()
		n := len(delivered)
		reportMu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	reportMu.Lock()
	reports := delivered
	reportMu.Unlock()

	if len(reports) == 0 {
		t.Fatal("expected at least one report delivery")
	}

	r := reports[0]
	if r.CronJobID != cronID {
		t.Errorf("CronJobID = %d, want %d", r.CronJobID, cronID)
	}
	if r.Status != "succeeded" {
		t.Errorf("Status = %q, want succeeded", r.Status)
	}
	if r.JobID == "" {
		t.Error("expected durable JobID to be set")
	}

	executedMu.Lock()
	tasks := executedTasks
	executedMu.Unlock()
	if len(tasks) == 0 {
		t.Error("expected LLM executor to have been called")
	}

	// Verify durable job was persisted with correct kind
	j, err := store.GetJob(r.JobID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if j == nil {
		t.Fatal("expected durable job to exist")
	}
	if j.Kind != "cron_llm" {
		t.Errorf("job kind = %q, want cron_llm", j.Kind)
	}
}

func TestSchedulerLegacyFallback(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	var notified []string
	var mu sync.Mutex

	sched := NewScheduler(store, nil)
	// No JobService set — should use legacy path
	sched.SetNotifier(func(chatID int64, message string) {
		mu.Lock()
		notified = append(notified, message)
		mu.Unlock()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sched.Stop()

	_, err := sched.AddExecJob("* * * * * *", "echo legacy-path", 42, 10)
	if err != nil {
		t.Fatalf("AddExecJob failed: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(notified)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	mu.Lock()
	msgs := notified
	mu.Unlock()

	if len(msgs) == 0 {
		t.Fatal("expected legacy notifier to have been called")
	}
}
