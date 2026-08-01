package worker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ok-gobot/internal/evidence"
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

type preflightStubAdapter struct {
	opts PreflightOptions
	runs atomic.Int32
}

func (s *preflightStubAdapter) PreflightOptions(_ Request) PreflightOptions {
	return s.opts
}

func (s *preflightStubAdapter) Run(_ context.Context, _ Request) (*Result, error) {
	s.runs.Add(1)
	return &Result{Content: "should not run"}, nil
}

func (s *preflightStubAdapter) Stream(_ context.Context, _ Request) <-chan Event {
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
	workDir := t.TempDir()
	runner := AdapterJobRunner(adapter, Request{Task: "build project", Model: "test-model", WorkDir: workDir})

	svc := runtime.NewJobService(store)
	job, err := svc.StartDetached(context.Background(), runtime.JobSpec{
		Kind:               "worker_task",
		Worker:             "stub",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "test bridge",
		ModelTier:          "test-tier",
		Branch:             "test-branch",
		WorktreePath:       workDir,
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

	events, err := store.ListEvidenceEventsForJob(job.JobID, 20)
	if err != nil {
		t.Fatalf("ListEvidenceEventsForJob failed: %v", err)
	}
	if got := countBridgeEvidenceEvents(events, evidence.EventBackendModel); got != 1 {
		t.Fatalf("backend/model evidence count = %d, want 1: %+v", got, events)
	}
	if got := countBridgeEvidenceEvents(events, evidence.EventWorkspace); got != 1 {
		t.Fatalf("workspace evidence count = %d, want 1: %+v", got, events)
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

func TestAdapterJobRunnerPreflightFailureRefusesWorker(t *testing.T) {
	t.Parallel()

	store := newBridgeTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "agent:test:telegram:group:202"
	if err := store.SaveSessionRoute(storage.SessionRoute{
		SessionKey: routeKey,
		Channel:    "telegram",
		ChatID:     202,
	}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	repo := newPreflightRepo(t)
	secret := "ghp_abcdefghijklmnopqrstuvwxyz123456"
	opts := passingPreflightOptions(repo)
	opts.CommandRunner = func(_ context.Context, _ string, name string, args ...string) CommandResult {
		if name == "gh" {
			return CommandResult{Stderr: "not authenticated token=" + secret, Err: errors.New("exit status 1")}
		}
		return passingCommandResult(repo, name, args...)
	}
	adapter := &preflightStubAdapter{opts: opts}
	runner := AdapterJobRunner(adapter, Request{Task: "build project", Model: "test-model", WorkDir: repo})

	svc := runtime.NewJobService(store)
	job, err := svc.StartDetached(context.Background(), runtime.JobSpec{
		Kind:               "worker_task",
		Worker:             "stub",
		SessionKey:         "agent:test:main",
		DeliverySessionKey: routeKey,
		Description:        "test preflight bridge",
		Timeout:            2 * time.Second,
		MaxAttempts:        2,
	}, runner)
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForBridgeJobStatus(t, store, job.JobID, string(runtime.JobStatusPreflightFailed))
	if adapter.runs.Load() != 0 {
		t.Fatalf("adapter ran %d time(s), want 0", adapter.runs.Load())
	}
	if finished.Error == "" {
		t.Fatal("expected preflight error to be stored")
	}
	if strings.Count(finished.Error, "preflight failed") != 1 {
		t.Fatalf("preflight error repeated headline: %q", finished.Error)
	}
	for _, want := range []string{"[github.auth] GitHub authentication is missing or invalid", "Hint: Run gh auth login"} {
		if !strings.Contains(finished.Error, want) {
			t.Fatalf("preflight error missing %q: %q", want, finished.Error)
		}
	}
	if strings.Contains(finished.Error, secret) {
		t.Fatalf("preflight error leaked secret: %q", finished.Error)
	}

	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Name != "preflight.json" || artifacts[0].ArtifactType != "preflight_evidence" {
		t.Fatalf("unexpected preflight artifacts: %+v", artifacts)
	}
	for _, want := range []string{`"id": "github.auth"`, `"reason": "GitHub authentication is missing or invalid"`, `"remediation": "Run gh auth login`} {
		if !strings.Contains(artifacts[0].Content, want) {
			t.Fatalf("preflight artifact missing %q: %s", want, artifacts[0].Content)
		}
	}
	if strings.Contains(artifacts[0].Content, secret) {
		t.Fatalf("preflight artifact leaked secret: %s", artifacts[0].Content)
	}

	events, err := store.ListJobEvents(job.JobID, 20)
	if err != nil {
		t.Fatalf("ListJobEvents failed: %v", err)
	}
	if !hasJobEvent(events, string(runtime.JobEventPreflightFailed)) {
		t.Fatalf("expected preflight_failed event, got %+v", events)
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

func hasJobEvent(events []storage.JobEvent, eventType string) bool {
	for _, event := range events {
		if event.EventType == eventType {
			return true
		}
	}
	return false
}

func countBridgeEvidenceEvents(events []evidence.Event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}
