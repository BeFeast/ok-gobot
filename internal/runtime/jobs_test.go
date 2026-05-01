package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	artifactview "ok-gobot/internal/artifacts"
	"ok-gobot/internal/delegation"
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

func TestJobServiceRoleSuccessExtractsReportAndURLArtifacts(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:        "role",
		Worker:      "test_runner",
		Description: "role:proof",
		RoleName:    "proof",
		Timeout:     2 * time.Second,
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{Summary: "Proof complete: https://example.com/proof."}, nil
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}
	waitForJobStatus(t, store, job.JobID, string(JobStatusSucceeded))

	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifact count = %d, want 2: %+v", len(artifacts), artifacts)
	}
	if artifacts[0].ArtifactType != JobArtifactTypeTextReport || artifacts[0].Content == "" {
		t.Fatalf("artifact[0] = %+v, want final text report", artifacts[0])
	}
	if artifacts[1].ArtifactType != JobArtifactTypeURL || artifacts[1].URI != "https://example.com/proof" {
		t.Fatalf("artifact[1] = %+v, want safe URL", artifacts[1])
	}
}

func TestJobServiceRoleSuccessPreservesBalancedURLDelimiters(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:        "role",
		Worker:      "test_runner",
		Description: "role:proof",
		RoleName:    "proof",
		Timeout:     2 * time.Second,
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{
			Summary: "Proof: https://en.wikipedia.org/wiki/Foo_(band) and https://example.com/docs/[draft]. Wrapped: https://example.com/trailing).",
		}, nil
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}
	waitForJobStatus(t, store, job.JobID, string(JobStatusSucceeded))

	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	gotURLs := map[string]bool{}
	for _, artifact := range artifacts {
		if artifact.ArtifactType == JobArtifactTypeURL {
			gotURLs[artifact.URI] = true
		}
	}
	for _, want := range []string{
		"https://en.wikipedia.org/wiki/Foo_(band)",
		"https://example.com/docs/[draft]",
		"https://example.com/trailing",
	} {
		if !gotURLs[want] {
			t.Fatalf("missing URL %q in artifacts: %+v", want, artifacts)
		}
	}
}

func TestJobServiceRoleSuccessPersistsSafeScreenshotArtifact(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	root := t.TempDir()
	shotPath := filepath.Join(root, "shot.png")
	if err := os.WriteFile(shotPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:          "role",
		Description:   "role:screenshot",
		RoleName:      "screenshot",
		Timeout:       2 * time.Second,
		ArtifactRoots: []string{root},
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{Summary: "Screenshot saved to " + shotPath}, nil
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}
	waitForJobStatus(t, store, job.JobID, string(JobStatusSucceeded))

	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	var screenshot *storage.JobArtifact
	for i := range artifacts {
		if artifacts[i].ArtifactType == JobArtifactTypeScreenshot {
			screenshot = &artifacts[i]
			break
		}
	}
	if screenshot == nil {
		t.Fatalf("missing screenshot artifact: %+v", artifacts)
	}
	info := artifactview.NewSerializer([]string{root}, "/api/artifacts").Serialize(*screenshot)
	if info.Display.Kind != artifactview.KindImage || !info.Display.Safe || !info.Display.Preview || info.Path != shotPath {
		t.Fatalf("screenshot is not display-safe: %+v", info)
	}
}

func TestJobServiceRoleSuccessRejectsUnsafeLocalArtifact(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("secret image bytes"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:          "role",
		Description:   "role:unsafe",
		RoleName:      "unsafe",
		Timeout:       2 * time.Second,
		ArtifactRoots: []string{root},
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{
			Summary: "done",
			Artifacts: []JobArtifactSpec{{
				Name:     "outside screenshot",
				Type:     JobArtifactTypeScreenshot,
				MimeType: "image/png",
				Content:  "secret image bytes",
				URI:      outside,
			}},
		}, nil
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}
	waitForJobStatus(t, store, job.JobID, string(JobStatusSucceeded))

	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactType == JobArtifactTypeScreenshot {
			t.Fatalf("unsafe screenshot was persisted: %+v", artifact)
		}
		if strings.Contains(artifact.URI, outside) || strings.Contains(artifact.Content, "secret image bytes") {
			t.Fatalf("unsafe artifact leaked path or content: %+v", artifact)
		}
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
	cancelRequestedIndex := -1
	cancelledIndex := -1
	for i, event := range events {
		switch event.EventType {
		case string(JobEventCancelRequested):
			cancelRequestedIndex = i
		case string(JobEventCancelled):
			cancelledIndex = i
		}
	}
	if cancelRequestedIndex == -1 {
		t.Fatalf("expected cancel_requested event, got %+v", events)
	}
	if cancelledIndex == -1 {
		t.Fatalf("expected cancelled event, got %+v", events)
	}
	if cancelRequestedIndex > cancelledIndex {
		t.Fatalf("expected cancel_requested before cancelled, got %+v", events)
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

func TestJobServiceRetryDetachedUsesRunnerArtifactRoots(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	root := t.TempDir()
	shotPath := filepath.Join(root, "retry-shot.png")
	if err := os.WriteFile(shotPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}

	svc := NewJobService(store)
	original, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:          "role",
		Worker:        "retry_runner",
		Description:   "role:proof",
		RoleName:      "proof",
		MaxAttempts:   2,
		ArtifactRoots: []string{root},
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{}, errors.New("boom")
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	waitForJobStatus(t, store, original.JobID, string(JobStatusFailed))

	retry, err := svc.RetryDetached(context.Background(), original.JobID, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{
			Summary:       "retried proof",
			ArtifactRoots: []string{root},
			Artifacts: []JobArtifactSpec{{
				Name:     "retry screenshot",
				Type:     JobArtifactTypeScreenshot,
				MimeType: "image/png",
				URI:      shotPath,
			}},
		}, nil
	})
	if err != nil {
		t.Fatalf("RetryDetached failed: %v", err)
	}

	waitForJobStatus(t, store, retry.JobID, string(JobStatusSucceeded))
	artifacts, err := store.ListJobArtifacts(retry.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactType == JobArtifactTypeScreenshot && artifact.URI == shotPath {
			return
		}
	}
	t.Fatalf("missing retry screenshot artifact rooted at %q: %+v", root, artifacts)
}

func TestJobServiceBudgetExceededMarksBudgetExceeded(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:88"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     88,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:               "background_task",
		Worker:             "budget_runner",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "hit budget",
		MaxToolCalls:       10,
		Timeout:            5 * time.Second,
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{Summary: "partial work done"}, &delegation.BudgetExceededError{
			Reason: delegation.LimitToolCalls,
			Report: delegation.RunReport{
				Status:        "budget_exceeded",
				LimitReason:   delegation.LimitToolCalls,
				ToolCallsUsed: 10,
				ToolCallMax:   10,
				Summary:       "partial work done",
			},
		}
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForJobStatus(t, store, job.JobID, string(JobStatusBudgetExceeded))
	if finished.LimitReason != string(delegation.LimitToolCalls) {
		t.Fatalf("LimitReason = %q, want %q", finished.LimitReason, delegation.LimitToolCalls)
	}
	if finished.Summary != "partial work done" {
		t.Fatalf("Summary = %q, want %q", finished.Summary, "partial work done")
	}

	events := waitForJobEvents(t, store, job.JobID, 3)
	lastEvent := events[len(events)-1]
	if lastEvent.EventType != string(JobEventBudgetExceeded) {
		t.Fatalf("expected final event budget_exceeded, got %q", lastEvent.EventType)
	}
}

func TestJobServiceBudgetExceededFallsBackToRunnerSummary(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:89"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     89,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:               "background_task",
		Worker:             "budget_runner",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "hit budget with runner summary",
		MaxToolCalls:       5,
		Timeout:            5 * time.Second,
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{Summary: "runner produced this"}, &delegation.BudgetExceededError{
			Reason: delegation.LimitToolCalls,
			Report: delegation.RunReport{
				Status:        "budget_exceeded",
				LimitReason:   delegation.LimitToolCalls,
				ToolCallsUsed: 5,
				ToolCallMax:   5,
				Summary:       "", // intentionally empty
			},
		}
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForJobStatus(t, store, job.JobID, string(JobStatusBudgetExceeded))
	if finished.Summary != "runner produced this" {
		t.Fatalf("Summary = %q, want %q (fallback to runner summary)", finished.Summary, "runner produced this")
	}
}

func TestJobServiceMaxToolCallsPersistedInSpec(t *testing.T) {
	t.Parallel()

	store := newRuntimeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:66"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     66,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	svc := NewJobService(store)
	job, err := svc.StartDetached(context.Background(), JobSpec{
		Kind:               "background_task",
		Worker:             "test_runner",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "budget test",
		MaxToolCalls:       25,
		Timeout:            2 * time.Second,
	}, func(ctx context.Context, job *storage.Job, svc *JobService) (JobRunResult, error) {
		return JobRunResult{Summary: "done"}, nil
	})
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForJobStatus(t, store, job.JobID, string(JobStatusSucceeded))
	if finished.MaxToolCalls != 25 {
		t.Fatalf("MaxToolCalls = %d, want 25", finished.MaxToolCalls)
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
