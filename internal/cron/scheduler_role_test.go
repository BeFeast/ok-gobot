package cron

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/role"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

// fakeAgentHub captures Submit/Cancel calls and replies with a configurable
// final RunEventDone payload so the scheduler can exercise the rolejob path
// without booting a real LLM runtime.
type fakeAgentHub struct {
	mu       sync.Mutex
	requests []agent.RunRequest
	content  string
}

func (h *fakeAgentHub) Submit(req agent.RunRequest) <-chan agent.RunEvent {
	h.mu.Lock()
	h.requests = append(h.requests, req)
	content := h.content
	h.mu.Unlock()

	ch := make(chan agent.RunEvent, 1)
	ch <- agent.RunEvent{Type: agent.RunEventDone, Result: &agent.AgentResponse{Message: content}, ProfileName: "test"}
	close(ch)
	return ch
}

func (h *fakeAgentHub) Cancel(agent.SessionKey) {}

func (h *fakeAgentHub) lastRequest(t *testing.T) agent.RunRequest {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) == 0 {
		t.Fatal("expected at least one agent submission")
	}
	return h.requests[len(h.requests)-1]
}

func TestSchedulerScheduledRoleUsesRoleRunner(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	jobService := runtime.NewJobService(store)

	var (
		mu        sync.Mutex
		delivered []JobReport
	)

	sched := NewScheduler(store, nil)
	sched.SetJobService(jobService)
	sched.SetReportDeliverer(func(_ int64, report JobReport) {
		mu.Lock()
		delivered = append(delivered, report)
		mu.Unlock()
	})

	hub := &fakeAgentHub{content: "real worker result with concrete details"}
	sched.SetRoleAgentSubmitter(hub)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := sched.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer sched.Stop()

	manifests := []*role.Manifest{
		{
			Name:           "scheduled-role",
			Schedule:       "* * * * * *",
			Prompt:         "Run scheduled work.",
			Worker:         "standard",
			MaxToolCalls:   9,
			MaxDuration:    90 * time.Second,
			ReportTemplate: "🛎️ {{.Title}}\n{{.Summary}}\nDate: {{.Date}}",
		},
	}
	if err := sched.RegisterRoleJobs(manifests, 4242); err != nil {
		t.Fatalf("RegisterRoleJobs failed: %v", err)
	}

	deadline := time.Now().Add(cronTestWait)
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
	reports := append([]JobReport(nil), delivered...)
	mu.Unlock()
	if len(reports) == 0 {
		t.Fatal("expected at least one report delivery")
	}

	rep := reports[0]
	if rep.Status != "succeeded" {
		t.Fatalf("Status = %q, want succeeded", rep.Status)
	}
	if rep.JobType != "role" {
		t.Fatalf("JobType = %q, want role", rep.JobType)
	}
	if !strings.Contains(rep.Summary, "real worker result with concrete details") {
		t.Errorf("Summary should embed worker output; got %q", rep.Summary)
	}
	if !strings.Contains(rep.Summary, "🛎️") {
		t.Errorf("Summary should be rendered through the report template; got %q", rep.Summary)
	}
	if strings.Contains(rep.Summary, "completed task") {
		t.Errorf("Summary should not be the legacy stub; got %q", rep.Summary)
	}

	if rep.JobID == "" {
		t.Fatal("expected JobID on report")
	}
	job, err := store.GetJob(rep.JobID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if job == nil {
		t.Fatal("expected durable job to be persisted")
	}
	if job.Kind != "cron_role" {
		t.Errorf("job.Kind = %q, want cron_role", job.Kind)
	}
	if job.RoleName != "scheduled-role" {
		t.Errorf("job.RoleName = %q, want scheduled-role", job.RoleName)
	}
	if job.Worker != "standard" {
		t.Errorf("job.Worker = %q, want standard", job.Worker)
	}
	if job.MaxToolCalls != 9 {
		t.Errorf("job.MaxToolCalls = %d, want 9", job.MaxToolCalls)
	}
	if job.TimeoutSeconds != 90 {
		t.Errorf("job.TimeoutSeconds = %d, want 90", job.TimeoutSeconds)
	}
	if !strings.Contains(job.Summary, "real worker result with concrete details") {
		t.Errorf("persisted summary should be the worker output; got %q", job.Summary)
	}
	if strings.HasPrefix(job.Summary, "completed task") {
		t.Errorf("persisted summary should not be the stub %q", job.Summary)
	}

	req := hub.lastRequest(t)
	if !strings.Contains(req.Content, "Run scheduled work.") {
		t.Errorf("agent submission should embed the manifest prompt; got %q", req.Content)
	}
	if req.Job == nil || req.Job.MaxToolCalls != 9 {
		t.Errorf("agent submission should carry budget fields; got %+v", req.Job)
	}
}

func TestSchedulerScheduledRoleFallsBackWithoutAgentSubmitter(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	jobService := runtime.NewJobService(store)

	var (
		mu        sync.Mutex
		delivered []JobReport
	)

	sched := NewScheduler(store, nil)
	sched.SetJobService(jobService)
	sched.SetReportDeliverer(func(_ int64, report JobReport) {
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

	manifests := []*role.Manifest{
		{Name: "legacy-role", Schedule: "* * * * * *", Prompt: "stub", MaxToolCalls: 3},
	}
	if err := sched.RegisterRoleJobs(manifests, 7); err != nil {
		t.Fatalf("RegisterRoleJobs failed: %v", err)
	}

	deadline := time.Now().Add(cronTestWait)
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
	reports := append([]JobReport(nil), delivered...)
	mu.Unlock()

	if len(reports) == 0 {
		t.Fatal("expected at least one report delivery")
	}
	rep := reports[0]
	job, err := store.GetJob(rep.JobID)
	if err != nil {
		t.Fatalf("GetJob failed: %v", err)
	}
	if job == nil {
		t.Fatal("expected durable job to be persisted")
	}
	// Without a role agent submitter the scheduler keeps the legacy LLM kind
	// so existing cron exec/LLM tooling and tests are not perturbed.
	if job.Kind != "cron_llm" {
		t.Errorf("expected cron_llm fallback kind; got %q", job.Kind)
	}
}

func TestRenderRoleReportSkipsForNonSuccess(t *testing.T) {
	t.Parallel()

	m := &role.Manifest{Name: "monitor", ReportTemplate: "👀 {{.Summary}}"}
	finished := &storage.Job{Status: string(runtime.JobStatusFailed), Summary: "boom"}
	if got := renderRoleReport(m, finished); got != "" {
		t.Errorf("renderRoleReport on failed job = %q, want empty", got)
	}

	finished.Status = string(runtime.JobStatusSucceeded)
	finished.Summary = "all good"
	got := renderRoleReport(m, finished)
	if !strings.Contains(got, "all good") || !strings.Contains(got, "👀") {
		t.Errorf("renderRoleReport on success = %q, want template applied", got)
	}
}
