package jobs

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ok-gobot/internal/storage"
	"ok-gobot/internal/workers"
)

type mockWorker struct {
	name string
	run  func(context.Context, workers.RunRequest, func(workers.RunUpdate)) (workers.RunResult, error)
}

func (m *mockWorker) Name() string        { return m.name }
func (m *mockWorker) Description() string { return "mock worker" }
func (m *mockWorker) Binary() string      { return m.name }
func (m *mockWorker) Run(ctx context.Context, req workers.RunRequest, emit func(workers.RunUpdate)) (workers.RunResult, error) {
	return m.run(ctx, req, emit)
}

func TestLaunchAndCompleteJob(t *testing.T) {
	t.Parallel()

	store, err := storage.New(filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	defer store.Close() //nolint:errcheck

	registry := workers.NewRegistry()
	registry.Register(&mockWorker{
		name: "mock",
		run: func(_ context.Context, req workers.RunRequest, emit func(workers.RunUpdate)) (workers.RunResult, error) {
			emit(workers.RunUpdate{Kind: "output", Message: "started"})
			return workers.RunResult{Output: "done: " + req.Prompt}, nil
		},
	}, true)

	service := NewService(store, registry, nil)
	job, err := service.Launch(context.Background(), LaunchRequest{
		SessionKey:     "dm:1",
		ChatID:         1,
		TaskType:       "router_job",
		Summary:        "Test job",
		RouterDecision: "launch_job",
		Input: InputPayload{
			Prompt: "hello",
		},
	})
	if err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	waitForJobStatus(t, store, job.ID, storage.JobDone)

	got, err := store.GetJob(job.ID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if got == nil || got.Status != storage.JobDone {
		t.Fatalf("unexpected final job: %+v", got)
	}

	events, err := store.ListJobEvents(job.ID)
	if err != nil {
		t.Fatalf("ListJobEvents failed: %v", err)
	}
	if len(events) < 3 {
		t.Fatalf("expected queued/running/output/done events, got %+v", events)
	}
}

func TestCancelQueuedJob(t *testing.T) {
	t.Parallel()

	store, err := storage.New(filepath.Join(t.TempDir(), "jobs-cancel.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	defer store.Close() //nolint:errcheck

	registry := workers.NewRegistry()
	registry.Register(&mockWorker{
		name: "slow",
		run: func(ctx context.Context, req workers.RunRequest, emit func(workers.RunUpdate)) (workers.RunResult, error) {
			select {
			case <-ctx.Done():
				return workers.RunResult{}, ctx.Err()
			case <-time.After(5 * time.Second):
				return workers.RunResult{Output: req.Prompt}, nil
			}
		},
	}, true)

	service := NewService(store, registry, nil)
	job, err := service.Launch(context.Background(), LaunchRequest{
		SessionKey:     "dm:9",
		ChatID:         9,
		RouterDecision: "launch_job",
		Input:          InputPayload{Prompt: "slow"},
	})
	if err != nil {
		t.Fatalf("Launch failed: %v", err)
	}

	if err := service.Cancel(job.ID); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	waitForJobStatus(t, store, job.ID, storage.JobCancelled)
}

func waitForJobStatus(t *testing.T, store *storage.Store, jobID int64, status storage.JobStatus) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.GetJob(jobID)
		if err != nil {
			t.Fatalf("GetJob failed: %v", err)
		}
		if job != nil && job.Status == status {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for job %d to reach %s", jobID, status)
}
