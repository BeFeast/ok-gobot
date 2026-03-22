package worker

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

type stubAdapter struct {
	runResult *Result
	runErr    error
}

func (s *stubAdapter) Run(_ context.Context, _ Request) (*Result, error) {
	return s.runResult, s.runErr
}

func (s *stubAdapter) Stream(_ context.Context, _ Request) <-chan Event {
	ch := make(chan Event)
	close(ch)
	return ch
}

func newBridgeTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "bridge-test.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	return store
}

func TestAdapterJobRunnerSuccess(t *testing.T) {
	t.Parallel()

	store := newBridgeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:200"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     200,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	adapter := &stubAdapter{
		runResult: &Result{Content: "task completed", SessionID: "sess-42"},
	}
	runner := AdapterJobRunner(adapter, Request{Task: "build project", Model: "test-model"})

	svc := runtime.NewJobService(store)
	job, err := svc.StartDetached(context.Background(), runtime.JobSpec{
		Kind:               "worker_task",
		Worker:             "stub",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "test bridge",
		Timeout:            2 * time.Second,
	}, runner)
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForBridgeJobStatus(t, store, job.JobID, string(runtime.JobStatusSucceeded))
	if finished.Summary != "task completed" {
		t.Fatalf("summary = %q, want %q", finished.Summary, "task completed")
	}

	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1", len(artifacts))
	}
	if artifacts[0].Name != "output" || artifacts[0].Content != "task completed" {
		t.Fatalf("unexpected artifact: %+v", artifacts[0])
	}
}

func TestAdapterJobRunnerFailure(t *testing.T) {
	t.Parallel()

	store := newBridgeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:201"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     201,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	adapter := &stubAdapter{
		runErr: errors.New("binary not found"),
	}
	runner := AdapterJobRunner(adapter, Request{Task: "fail task"})

	svc := runtime.NewJobService(store)
	job, err := svc.StartDetached(context.Background(), runtime.JobSpec{
		Kind:               "worker_task",
		Worker:             "stub",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "test failure bridge",
		Timeout:            2 * time.Second,
	}, runner)
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForBridgeJobStatus(t, store, job.JobID, string(runtime.JobStatusFailed))
	if finished.Error == "" {
		t.Fatal("expected error to be stored")
	}
}

func waitForBridgeJobStatus(t *testing.T, store *storage.Store, jobID, want string) *storage.Job {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.GetJob(jobID)
		if err != nil {
			t.Fatalf("GetJob failed: %v", err)
		}
		if job != nil && job.Status == want {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for job %s to reach status %s", jobID, want)
	return nil
}
