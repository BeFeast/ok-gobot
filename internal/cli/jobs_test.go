package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func seedJob(t *testing.T, store *storage.Store, job storage.Job) {
	t.Helper()
	if err := store.CreateJob(job); err != nil {
		t.Fatalf("CreateJob(%s) error = %v", job.JobID, err)
	}
}

func newTestStore(t *testing.T) (*storage.Store, *config.Config) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cli.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() { store.Close() }) //nolint:errcheck
	cfg := &config.Config{StoragePath: dbPath}
	return store, cfg
}

func TestJobsList_Empty(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}
	if !strings.Contains(out.String(), "No jobs found") {
		t.Fatalf("expected 'No jobs found', got: %q", out.String())
	}
}

func TestJobsList_ShowsJobs(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:       "job-test-1",
		Kind:        "research",
		Worker:      "agent-a",
		Description: "test job one",
		Status:      "running",
	})
	seedJob(t, store, storage.Job{
		JobID:       "job-test-2",
		Kind:        "summary",
		Worker:      "agent-b",
		Description: "test job two",
		Status:      "succeeded",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "job-test-1") {
		t.Errorf("expected job-test-1 in output: %q", output)
	}
	if !strings.Contains(output, "job-test-2") {
		t.Errorf("expected job-test-2 in output: %q", output)
	}
	if !strings.Contains(output, "running") {
		t.Errorf("expected 'running' in output: %q", output)
	}
}

func TestJobsList_FilterByStatus(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:  "job-run-1",
		Kind:   "research",
		Status: "running",
	})
	seedJob(t, store, storage.Job{
		JobID:  "job-done-1",
		Kind:   "research",
		Status: "succeeded",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--status", "running"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "job-run-1") {
		t.Errorf("expected job-run-1 in output: %q", output)
	}
	if strings.Contains(output, "job-done-1") {
		t.Errorf("did not expect job-done-1 in output: %q", output)
	}
}

func TestJobsInspect(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:       "job-inspect-1",
		Kind:        "research",
		Worker:      "agent-x",
		Description: "deep research task",
		Status:      "succeeded",
		Summary:     "all done",
		Attempt:     1,
		MaxAttempts: 3,
	})
	if err := store.AddJobEvent(storage.JobEvent{
		JobID:     "job-inspect-1",
		EventType: "created",
		Message:   "deep research task",
	}); err != nil {
		t.Fatalf("AddJobEvent error = %v", err)
	}

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"inspect", "job-inspect-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	for _, want := range []string{"job-inspect-1", "succeeded", "research", "agent-x", "deep research task", "1 / 3", "all done", "Events:", "created"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in output: %q", want, output)
		}
	}
}

func TestJobsInspect_NotFound(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"inspect", "nonexistent"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestJobsCancel_Pending(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:  "job-cancel-1",
		Kind:   "research",
		Status: "pending",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cancel", "job-cancel-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if !strings.Contains(out.String(), "cancelled") {
		t.Errorf("expected 'cancelled' in output: %q", out.String())
	}

	// Verify DB state.
	job, err := store.GetJob("job-cancel-1")
	if err != nil {
		t.Fatalf("GetJob error = %v", err)
	}
	if job.Status != "cancelled" {
		t.Errorf("expected status=cancelled, got %q", job.Status)
	}
}

func TestJobsCancel_Running(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:  "job-cancel-2",
		Kind:   "research",
		Status: "running",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cancel", "job-cancel-2"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if !strings.Contains(out.String(), "Cancellation requested") {
		t.Errorf("expected 'Cancellation requested' in output: %q", out.String())
	}

	// Verify cancel_requested flag.
	job, err := store.GetJob("job-cancel-2")
	if err != nil {
		t.Fatalf("GetJob error = %v", err)
	}
	if !job.CancelRequested {
		t.Error("expected cancel_requested=true")
	}
}

func TestJobsCancel_AlreadyTerminal(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:  "job-cancel-3",
		Kind:   "research",
		Status: "succeeded",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"cancel", "job-cancel-3"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for already terminal job")
	}
	if !strings.Contains(err.Error(), "terminal state") {
		t.Fatalf("expected 'terminal state' error, got: %v", err)
	}
}

func TestJobsRetry(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:       "job-retry-1",
		Kind:        "research",
		Worker:      "agent-a",
		Description: "failed task",
		Status:      "failed",
		Attempt:     1,
		MaxAttempts: 3,
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"retry", "job-retry-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "Retry queued") {
		t.Errorf("expected 'Retry queued' in output: %q", output)
	}
	if !strings.Contains(output, "attempt 2 of 3") {
		t.Errorf("expected 'attempt 2 of 3' in output: %q", output)
	}

	// Verify a retry event was recorded on the original job.
	events, err := store.ListJobEvents("job-retry-1", 10)
	if err != nil {
		t.Fatalf("ListJobEvents error = %v", err)
	}
	found := false
	for _, e := range events {
		if e.EventType == "retry_requested" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected retry_requested event on original job")
	}
}

func TestJobsRetry_StillRunning(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:  "job-retry-2",
		Kind:   "research",
		Status: "running",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"retry", "job-retry-2"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for running job")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Fatalf("expected 'still running' error, got: %v", err)
	}
}

func TestJobsRetry_MaxAttemptsReached(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:       "job-retry-3",
		Kind:        "research",
		Status:      "failed",
		Attempt:     3,
		MaxAttempts: 3,
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"retry", "job-retry-3"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for max attempts")
	}
	if !strings.Contains(err.Error(), "max attempts") {
		t.Fatalf("expected 'max attempts' error, got: %v", err)
	}
}

func TestJobsTail(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:  "job-tail-1",
		Kind:   "research",
		Status: "succeeded",
	})
	for _, evt := range []storage.JobEvent{
		{JobID: "job-tail-1", EventType: "created", Message: "started research"},
		{JobID: "job-tail-1", EventType: "progress", Message: "halfway there"},
		{JobID: "job-tail-1", EventType: "succeeded", Message: "all done"},
	} {
		if err := store.AddJobEvent(evt); err != nil {
			t.Fatalf("AddJobEvent error = %v", err)
		}
	}

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"tail", "job-tail-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	for _, want := range []string{"created", "progress", "succeeded", "started research", "halfway there", "all done"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in output: %q", want, output)
		}
	}
}

func TestJobsList_ShowsRoleAndDuration(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:    "job-role-1",
		Kind:     "research",
		Worker:   "agent-a",
		Status:   "succeeded",
		RoleName: "researcher",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "researcher") {
		t.Errorf("expected 'researcher' in output: %q", output)
	}
	if !strings.Contains(output, "ROLE") {
		t.Errorf("expected ROLE header in output: %q", output)
	}
}

func TestJobsInspect_ShowsRoleAndModelTier(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:     "job-meta-1",
		Kind:      "research",
		Worker:    "agent-x",
		Status:    "running",
		RoleName:  "auditor",
		ModelTier: "premium/claude-opus",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"inspect", "job-meta-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	for _, want := range []string{"auditor", "premium/claude-opus", "Role:", "Model/Tier:"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in output: %q", want, output)
		}
	}
}

func TestJobsInspect_ShowsArtifactsWithSafeURIs(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	root := t.TempDir()
	cfg.Artifacts.Roots = []string{root}
	shotPath := filepath.Join(root, "shot.png")
	if err := os.WriteFile(shotPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("png"), 0o644); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	seedJob(t, store, storage.Job{
		JobID:    "job-artifacts-1",
		Kind:     "role",
		Status:   "succeeded",
		RoleName: "auditor",
	})
	if err := store.AddJobArtifact(storage.JobArtifact{JobID: "job-artifacts-1", Name: "PR", ArtifactType: "url", URI: "https://example.com/pr/1"}); err != nil {
		t.Fatalf("AddJobArtifact url: %v", err)
	}
	if err := store.AddJobArtifact(storage.JobArtifact{JobID: "job-artifacts-1", Name: "Screenshot", ArtifactType: "screenshot", MimeType: "image/png", URI: shotPath}); err != nil {
		t.Fatalf("AddJobArtifact screenshot: %v", err)
	}
	if err := store.AddJobArtifact(storage.JobArtifact{JobID: "job-artifacts-1", Name: "Outside", ArtifactType: "screenshot", MimeType: "image/png", URI: outside}); err != nil {
		t.Fatalf("AddJobArtifact outside: %v", err)
	}

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"inspect", "job-artifacts-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	for _, want := range []string{"Artifacts:", "PR", "https://example.com/pr/1", "Screenshot", shotPath, "[unsafe:"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in output: %q", want, output)
		}
	}
	if strings.Contains(output, outside) {
		t.Fatalf("unsafe path leaked in output: %q", output)
	}
}

func TestJobsInspect_CancelReason(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:  "job-cancelled-1",
		Kind:   "research",
		Status: "cancelled",
		Error:  "operator requested stop",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"inspect", "job-cancelled-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, "Cancel reason:") {
		t.Errorf("expected 'Cancel reason:' in output: %q", output)
	}
	if !strings.Contains(output, "operator requested stop") {
		t.Errorf("expected error message in output: %q", output)
	}
}

func TestJobsExport_Markdown(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:       "job-export-md",
		Kind:        "research",
		Worker:      "agent-a",
		Description: "markdown export test",
		Status:      "succeeded",
		Summary:     "everything passed",
		RoleName:    "researcher",
		ModelTier:   "standard/gpt-4",
	})
	if err := store.AddJobEvent(storage.JobEvent{
		JobID:     "job-export-md",
		EventType: "created",
		Message:   "started",
	}); err != nil {
		t.Fatalf("AddJobEvent error = %v", err)
	}

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"export", "job-export-md", "--format", "markdown"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	for _, want := range []string{
		"# Job: job-export-md",
		"| Status | succeeded |",
		"| Role | researcher |",
		"| Model/Tier | standard/gpt-4 |",
		"## Summary",
		"everything passed",
		"## Events",
		"| created |",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("expected %q in output: %q", want, output)
		}
	}
}

func TestJobsExport_JSON(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:       "job-export-json",
		Kind:        "research",
		Status:      "failed",
		Description: "json export test",
		Error:       "something broke",
		RoleName:    "auditor",
	})

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"export", "job-export-json", "--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	output := out.String()
	if !strings.Contains(output, `"job-export-json"`) {
		t.Errorf("expected job ID in JSON output: %q", output)
	}
	if !strings.Contains(output, `"auditor"`) {
		t.Errorf("expected role name in JSON output: %q", output)
	}
}

func TestJobsExport_NotFound(t *testing.T) {
	t.Parallel()
	_, cfg := newTestStore(t)

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"export", "nonexistent"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected 'not found' error, got: %v", err)
	}
}

func TestJobsTail_Follow(t *testing.T) {
	t.Parallel()
	store, cfg := newTestStore(t)

	seedJob(t, store, storage.Job{
		JobID:  "job-tail-f",
		Kind:   "research",
		Status: "succeeded",
	})
	if err := store.AddJobEvent(storage.JobEvent{
		JobID:     "job-tail-f",
		EventType: "succeeded",
		Message:   "done",
	}); err != nil {
		t.Fatalf("AddJobEvent error = %v", err)
	}

	cmd := newJobsCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{"tail", "--follow", "job-tail-f"})

	// Job is already terminal, so --follow should exit after printing events.
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute error = %v", err)
	}

	if !strings.Contains(out.String(), "succeeded") {
		t.Errorf("expected 'succeeded' in output: %q", out.String())
	}
}
