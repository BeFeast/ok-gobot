package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"ok-gobot/internal/storage"
)

func TestJobServiceStartDetachedPersistsSuccess(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:42"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     42,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:               "background_task",
		Worker:             "test_runner",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "collect diagnostics",
		Timeout:            2 * time.Second,
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		if err := svc.AppendEvent(job.JobID, JobEventProgress, "halfway", map[string]any{"percent": 50}); err != nil {
			return JobRunResult{}, err
		}
		return JobRunResult{
			Summary: "done",
			Artifacts: []JobArtifactSpec{
				{
					Name:     "result.md",
					Type:     "report",
					MimeType: "text/markdown",
					Content:  "# done",
					Metadata: map[string]any{"source": "test"},
				},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForJobStatus(t, store, job.JobID, string(JobStatusSucceeded))
	if finished.Summary != "done" {
		t.Fatalf("summary mismatch: got %q", finished.Summary)
	}

	events := waitForJobEvents(t, store, job.JobID, 5)
	wantEvents := []string{
		string(JobEventCreated),
		string(JobEventStarted),
		string(JobEventProgress),
		string(JobEventArtifactAdded),
		string(JobEventSucceeded),
	}
	if len(events) != len(wantEvents) {
		t.Fatalf("event count mismatch: got %d want %d (%+v)", len(events), len(wantEvents), events)
	}
	for i, want := range wantEvents {
		if events[i].EventType != want {
			t.Fatalf("event[%d] = %q want %q", i, events[i].EventType, want)
		}
	}

	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifact count mismatch: got %d", len(artifacts))
	}
	if artifacts[0].Name != "result.md" || artifacts[0].ArtifactType != "report" {
		t.Fatalf("unexpected artifact row: %+v", artifacts[0])
	}
}

func TestJobServiceCancelMarksCancelled(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:99"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     99,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:               "background_task",
		Worker:             "cancel_runner",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "wait forever",
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		<-ctx.Done()
		return JobRunResult{}, ctx.Err()
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	if _, err := waitForAnyStatus(t, store, job.JobID, string(JobStatusRunning), string(JobStatusCancelled)); err != nil {
		t.Fatalf("wait for running failed: %v", err)
	}
	if err := svc.Cancel(job.JobID); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}

	finished := waitForJobStatus(t, store, job.JobID, string(JobStatusCancelled))
	if !finished.CancelRequested {
		t.Fatal("expected CancelRequested=true")
	}

	events := waitForJobEvents(t, store, job.JobID, 4)
	if events[len(events)-2].EventType != string(JobEventCancelRequested) {
		t.Fatalf("expected penultimate event cancel_requested, got %+v", events)
	}
	if events[len(events)-1].EventType != string(JobEventCancelled) {
		t.Fatalf("expected final event cancelled, got %+v", events)
	}
}

func TestJobServiceTimeoutMarksTimedOut(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:77"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     77,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:               "background_task",
		Worker:             "timeout_runner",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "sleep until timeout",
		Timeout:            50 * time.Millisecond,
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		<-ctx.Done()
		return JobRunResult{}, ctx.Err()
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForJobStatus(t, store, job.JobID, string(JobStatusTimedOut))
	if finished.Error == "" {
		t.Fatal("expected timeout error to be stored")
	}
}

func TestJobServiceRetryDetachedClonesAttempt(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:55"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     55,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	svc := NewJobService(store)
	original, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:               "background_task",
		Worker:             "retry_runner",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "fail once",
		MaxAttempts:        2,
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{}, errors.New("boom")
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	waitForJobStatus(t, store, original.JobID, string(JobStatusFailed))

	retry, err := svc.RetryDetached(context.Background(), original.JobID, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{Summary: "retried successfully"}, nil
	})
	if err != nil {
		t.Fatalf("RetryDetached failed: %v", err)
	}

	retried := waitForJobStatus(t, store, retry.JobID, string(JobStatusSucceeded))
	if retried.RetryOfJobID != original.JobID {
		t.Fatalf("RetryOfJobID mismatch: got %q want %q", retried.RetryOfJobID, original.JobID)
	}
	if retried.Attempt != 2 {
		t.Fatalf("retry attempt mismatch: got %d want 2", retried.Attempt)
	}

	events := waitForJobEvents(t, store, original.JobID, 4)
	if events[len(events)-1].EventType != string(JobEventRetryRequested) {
		t.Fatalf("expected final original event retry_requested, got %+v", events)
	}
}

func newRuntimeTestStore(t *testing.T) *storage.Store {
	t.Helper()

	store, err := storage.New(filepath.Join(t.TempDir(), "runtime-jobs.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	return store
}

func waitForJobStatus(t *testing.T, store *storage.Store, jobID, want string) *storage.Job {
	t.Helper()

	job, err := waitForAnyStatus(t, store, jobID, want)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func waitForAnyStatus(t *testing.T, store *storage.Store, jobID string, statuses ...string) (*storage.Job, error) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := store.GetJob(jobID)
		if err != nil {
			return nil, err
		}
		if job != nil {
			for _, status := range statuses {
				if job.Status == status {
					return job, nil
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil, errors.New("timed out waiting for job status")
}

func waitForJobEvents(t *testing.T, store *storage.Store, jobID string, want int) []storage.JobEvent {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		events, err := store.ListJobEvents(jobID, want)
		if err != nil {
			t.Fatalf("ListJobEvents failed: %v", err)
		}
		if len(events) >= want {
			return events
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d job events", want)
	return nil
}
