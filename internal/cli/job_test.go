package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func setupTestStore(t *testing.T) (*config.Config, *storage.Store) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfg := &config.Config{StoragePath: dbPath}

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	return cfg, store
}

func seedJob(t *testing.T, store *storage.Store, jobID, kind, worker, status string) {
	t.Helper()
	err := store.CreateJob(storage.Job{
		JobID:       jobID,
		Kind:        kind,
		Worker:      worker,
		Status:      "pending",
		Description: "test job " + jobID,
		Attempt:     1,
		MaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("failed to create job: %v", err)
	}

	switch status {
	case "running":
		if err := store.MarkJobRunning(jobID); err != nil {
			t.Fatalf("failed to mark running: %v", err)
		}
	case "succeeded":
		if err := store.MarkJobRunning(jobID); err != nil {
			t.Fatalf("failed to mark running: %v", err)
		}
		if err := store.MarkJobSucceeded(jobID, "done"); err != nil {
			t.Fatalf("failed to mark succeeded: %v", err)
		}
	case "failed":
		if err := store.MarkJobRunning(jobID); err != nil {
			t.Fatalf("failed to mark running: %v", err)
		}
		if err := store.MarkJobFailed(jobID, "something went wrong"); err != nil {
			t.Fatalf("failed to mark failed: %v", err)
		}
	}
}

func TestJobList(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-test-1", "research", "bot-a", "running")
	seedJob(t, store, "job-test-2", "compile", "bot-b", "succeeded")
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("job list failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "job-test-1") {
		t.Errorf("expected job-test-1 in output, got: %s", out)
	}
	if !strings.Contains(out, "job-test-2") {
		t.Errorf("expected job-test-2 in output, got: %s", out)
	}
}

func TestJobListFilterStatus(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-test-a", "research", "bot-a", "running")
	seedJob(t, store, "job-test-b", "compile", "bot-b", "succeeded")
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"list", "--status", "running"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("job list failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "job-test-a") {
		t.Errorf("expected job-test-a in output, got: %s", out)
	}
	if strings.Contains(out, "job-test-b") {
		t.Errorf("should not contain job-test-b, got: %s", out)
	}
}

func TestJobListJSON(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-json-1", "research", "bot-a", "pending")
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"list", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("job list json failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"job_id"`) {
		t.Errorf("expected JSON output, got: %s", out)
	}
	if !strings.Contains(out, "job-json-1") {
		t.Errorf("expected job-json-1 in output, got: %s", out)
	}
}

func TestJobInspect(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-inspect-1", "research", "bot-a", "running")
	_ = store.AddJobEvent(storage.JobEvent{
		JobID:     "job-inspect-1",
		EventType: "created",
		Message:   "job created",
	})
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"inspect", "job-inspect-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("job inspect failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "job-inspect-1") {
		t.Errorf("expected job ID in output, got: %s", out)
	}
	if !strings.Contains(out, "research") {
		t.Errorf("expected kind in output, got: %s", out)
	}
	if !strings.Contains(out, "Events") {
		t.Errorf("expected events section in output, got: %s", out)
	}
}

func TestJobInspectNotFound(t *testing.T) {
	cfg, store := setupTestStore(t)
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"inspect", "nonexistent"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for nonexistent job")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected not found error, got: %v", err)
	}
}

func TestJobCancel(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-cancel-1", "research", "bot-a", "running")
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"cancel", "job-cancel-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("job cancel failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Cancellation requested") {
		t.Errorf("expected cancellation message, got: %s", out)
	}

	// Verify DB state.
	store2, _ := storage.New(cfg.StoragePath)
	defer store2.Close()
	job, _ := store2.GetJob("job-cancel-1")
	if !job.CancelRequested {
		t.Error("expected cancel_requested to be true")
	}
}

func TestJobCancelPending(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-cancel-p", "research", "bot-a", "pending")
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"cancel", "job-cancel-p"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("job cancel failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "cancelled (was pending)") {
		t.Errorf("expected pending cancel message, got: %s", out)
	}

	store2, _ := storage.New(cfg.StoragePath)
	defer store2.Close()
	job, _ := store2.GetJob("job-cancel-p")
	if job.Status != "cancelled" {
		t.Errorf("expected status cancelled, got: %s", job.Status)
	}
}

func TestJobCancelAlreadyTerminal(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-cancel-done", "research", "bot-a", "succeeded")
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"cancel", "job-cancel-done"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for terminal job")
	}
	if !strings.Contains(err.Error(), "already succeeded") {
		t.Errorf("expected already terminal error, got: %v", err)
	}
}

func TestJobRetry(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-retry-1", "research", "bot-a", "failed")
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"retry", "job-retry-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("job retry failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Retry job") {
		t.Errorf("expected retry message, got: %s", out)
	}
	if !strings.Contains(out, "attempt 2") {
		t.Errorf("expected attempt 2, got: %s", out)
	}
}

func TestJobRetryStillRunning(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-retry-run", "research", "bot-a", "running")
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"retry", "job-retry-run"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for running job")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Errorf("expected still running error, got: %v", err)
	}
}

func TestJobTail(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-tail-1", "research", "bot-a", "succeeded")
	_ = store.AddJobEvent(storage.JobEvent{
		JobID:     "job-tail-1",
		EventType: "created",
		Message:   "job created",
	})
	_ = store.AddJobEvent(storage.JobEvent{
		JobID:     "job-tail-1",
		EventType: "started",
		Message:   "job started",
	})
	store.Close()

	cmd := newJobCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"tail", "job-tail-1"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("job tail failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "created") {
		t.Errorf("expected created event, got: %s", out)
	}
	if !strings.Contains(out, "started") {
		t.Errorf("expected started event, got: %s", out)
	}
}

func TestWorkerList(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-w-1", "research", "bot-alpha", "running")
	seedJob(t, store, "job-w-2", "compile", "bot-alpha", "succeeded")
	seedJob(t, store, "job-w-3", "research", "bot-beta", "failed")
	store.Close()

	cmd := newWorkerCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("worker list failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "bot-alpha") {
		t.Errorf("expected bot-alpha in output, got: %s", out)
	}
	if !strings.Contains(out, "bot-beta") {
		t.Errorf("expected bot-beta in output, got: %s", out)
	}
}

func TestWorkerInspect(t *testing.T) {
	cfg, store := setupTestStore(t)
	defer store.Close()

	seedJob(t, store, "job-wi-1", "research", "bot-alpha", "running")
	seedJob(t, store, "job-wi-2", "compile", "bot-beta", "succeeded")
	store.Close()

	cmd := newWorkerCommand(cfg)
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"inspect", "bot-alpha"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("worker inspect failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "job-wi-1") {
		t.Errorf("expected job-wi-1 in output, got: %s", out)
	}
	if strings.Contains(out, "job-wi-2") {
		t.Errorf("should not contain job-wi-2, got: %s", out)
	}
}

// Helpers

func TestFormatStatus(t *testing.T) {
	tests := []struct {
		job    storage.Job
		expect string
	}{
		{storage.Job{Status: "running"}, "running"},
		{storage.Job{Status: "running", CancelRequested: true}, "running (cancel)"},
		{storage.Job{Status: "succeeded"}, "succeeded"},
		{storage.Job{Status: "failed"}, "failed"},
	}

	for _, tt := range tests {
		got := formatStatus(tt.job)
		if got != tt.expect {
			t.Errorf("formatStatus(%s, cancel=%v) = %q, want %q",
				tt.job.Status, tt.job.CancelRequested, got, tt.expect)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		expect string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 10, "this is..."},
		{"ab", 1, "a"},
	}

	for _, tt := range tests {
		got := truncate(tt.input, tt.maxLen)
		if got != tt.expect {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.expect)
		}
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
