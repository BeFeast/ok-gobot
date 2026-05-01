package rolejob

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/role"
	jobruntime "ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

type fakeAgentHub struct {
	mu       sync.Mutex
	requests []agent.RunRequest
	content  string
	err      error
	block    bool
	cancel   agent.SessionKey
	events   []agent.ToolEvent
}

func (h *fakeAgentHub) Submit(req agent.RunRequest) <-chan agent.RunEvent {
	h.mu.Lock()
	h.requests = append(h.requests, req)
	h.mu.Unlock()

	ch := make(chan agent.RunEvent, 1)
	if h.block {
		return ch
	}
	for _, event := range h.events {
		if req.OnToolEvent != nil {
			req.OnToolEvent(event)
		}
	}
	if h.err != nil {
		ch <- agent.RunEvent{Type: agent.RunEventError, Err: h.err, ProfileName: "test"}
	} else {
		ch <- agent.RunEvent{Type: agent.RunEventDone, Result: &agent.AgentResponse{Message: h.content}, ProfileName: "test"}
	}
	close(ch)
	return ch
}

func (h *fakeAgentHub) Cancel(key agent.SessionKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cancel = key
}

func (h *fakeAgentHub) firstRequest(t *testing.T) agent.RunRequest {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.requests) == 0 {
		t.Fatal("expected one agent request")
	}
	return h.requests[0]
}

func TestAgentJobRunnerPersistsWorkerResultAndMetadata(t *testing.T) {
	t.Parallel()

	store := newRoleJobTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "dm:42"
	if err := store.SaveSessionRoute(storage.SessionRoute{SessionKey: routeKey, Channel: "telegram", ChatID: 42}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	manifest := &role.Manifest{
		Name:         "researcher",
		Prompt:       "Research the topic and produce a brief.",
		Worker:       "standard",
		Tools:        []string{"web_fetch"},
		MaxToolCalls: 7,
		MaxDuration:  90 * time.Second,
	}
	hub := &fakeAgentHub{content: "real worker result\n\nProof: https://example.com/report"}
	opts := Options{SessionKey: routeKey, DeliverySessionKey: routeKey, ChatID: 42}
	spec, err := JobSpec(manifest, opts)
	if err != nil {
		t.Fatalf("JobSpec failed: %v", err)
	}

	svc := jobruntime.NewJobService(store)
	job, err := svc.StartDetached(context.Background(), spec, AgentJobRunner(hub, manifest, "Go programming news", opts))
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForRoleJobStatus(t, store, job.JobID, string(jobruntime.JobStatusSucceeded))
	if finished.Summary != "real worker result\n\nProof: https://example.com/report" {
		t.Fatalf("Summary = %q, want worker result", finished.Summary)
	}
	if strings.Contains(finished.Summary, "prompt length") {
		t.Fatalf("summary still looks like the old stub: %q", finished.Summary)
	}
	if finished.Kind != "role" {
		t.Fatalf("Kind = %q, want role", finished.Kind)
	}
	if finished.RoleName != "researcher" {
		t.Fatalf("RoleName = %q, want researcher", finished.RoleName)
	}
	if finished.Worker != "standard" {
		t.Fatalf("Worker = %q, want standard", finished.Worker)
	}
	if finished.SessionKey != routeKey || finished.DeliverySessionKey != routeKey {
		t.Fatalf("session metadata = (%q, %q), want %q", finished.SessionKey, finished.DeliverySessionKey, routeKey)
	}
	if finished.MaxToolCalls != 7 {
		t.Fatalf("MaxToolCalls = %d, want 7", finished.MaxToolCalls)
	}
	if finished.TimeoutSeconds != 90 {
		t.Fatalf("TimeoutSeconds = %d, want 90", finished.TimeoutSeconds)
	}

	req := hub.firstRequest(t)
	if !strings.Contains(req.Content, manifest.Prompt) || !strings.Contains(req.Content, "User input: Go programming news") {
		t.Fatalf("agent request content did not include prompt and input: %q", req.Content)
	}
	if req.Job == nil || req.Job.MaxToolCalls != 7 || len(req.Job.ToolAllowlist) != 1 || req.Job.ToolAllowlist[0] != "web_fetch" {
		t.Fatalf("unexpected delegation job: %+v", req.Job)
	}
	if req.MemoryScope.RoleName != "researcher" || req.MemoryScope.JobID != job.JobID || req.MemoryScope.SessionKey != routeKey {
		t.Fatalf("unexpected memory scope: %+v", req.MemoryScope)
	}
	if string(req.SessionKey) == routeKey {
		t.Fatalf("runtime session key should be isolated from delivery session key")
	}

	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("artifact count = %d, want text report and URL: %+v", len(artifacts), artifacts)
	}
	if artifacts[0].ArtifactType != jobruntime.JobArtifactTypeTextReport || !strings.Contains(artifacts[0].Content, "real worker result") {
		t.Fatalf("artifact[0] = %+v, want text report", artifacts[0])
	}
	if artifacts[1].ArtifactType != jobruntime.JobArtifactTypeURL || artifacts[1].URI != "https://example.com/report" {
		t.Fatalf("artifact[1] = %+v, want URL", artifacts[1])
	}
}

func TestAgentJobRunnerPersistsFrontendVerifyScreenshot(t *testing.T) {
	t.Parallel()

	store := newRoleJobTestStore(t)
	defer store.Close() //nolint:errcheck

	root := t.TempDir()
	shotPath := filepath.Join(root, "proof.png")
	if err := os.WriteFile(shotPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	fullOutput := `{"match":true,"feedback":"` + strings.Repeat("verbose ", 60) + `","screenshot_path":"` + shotPath + `"}`
	truncatedOutput := fullOutput[:300] + "…"

	manifest := &role.Manifest{Name: "prototype", Prompt: "Build and verify UI", Worker: "standard"}
	hub := &fakeAgentHub{
		content: "verified",
		events: []agent.ToolEvent{{
			ToolName:   "frontend_verify",
			Type:       agent.ToolEventFinished,
			Output:     truncatedOutput,
			FullOutput: fullOutput,
		}},
	}
	opts := Options{ArtifactRoots: []string{root}}
	spec, err := JobSpec(manifest, opts)
	if err != nil {
		t.Fatalf("JobSpec failed: %v", err)
	}

	svc := jobruntime.NewJobService(store)
	job, err := svc.StartDetached(context.Background(), spec, AgentJobRunner(hub, manifest, "", opts))
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}
	waitForRoleJobStatus(t, store, job.JobID, string(jobruntime.JobStatusSucceeded))

	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}
	found := false
	for _, artifact := range artifacts {
		if artifact.ArtifactType == jobruntime.JobArtifactTypeScreenshot {
			found = true
			if artifact.URI != shotPath || artifact.Content != "" {
				t.Fatalf("unexpected screenshot artifact: %+v", artifact)
			}
		}
	}
	if !found {
		t.Fatalf("missing screenshot artifact: %+v", artifacts)
	}
}

func TestAgentJobRunnerPropagatesWorkerError(t *testing.T) {
	t.Parallel()

	store := newRoleJobTestStore(t)
	defer store.Close() //nolint:errcheck

	manifest := &role.Manifest{Name: "researcher", Prompt: "Do work", Worker: "standard"}
	hub := &fakeAgentHub{err: errors.New("worker failed")}
	spec, err := JobSpec(manifest, Options{})
	if err != nil {
		t.Fatalf("JobSpec failed: %v", err)
	}

	svc := jobruntime.NewJobService(store)
	job, err := svc.StartDetached(context.Background(), spec, AgentJobRunner(hub, manifest, "", Options{}))
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForRoleJobStatus(t, store, job.JobID, string(jobruntime.JobStatusFailed))
	if !strings.Contains(finished.Error, "worker failed") {
		t.Fatalf("Error = %q, want worker failure", finished.Error)
	}
}

func TestAgentJobRunnerPropagatesTimeout(t *testing.T) {
	t.Parallel()

	store := newRoleJobTestStore(t)
	defer store.Close() //nolint:errcheck

	manifest := &role.Manifest{Name: "researcher", Prompt: "Do work", Worker: "standard", MaxDuration: 30 * time.Millisecond}
	hub := &fakeAgentHub{block: true}
	spec, err := JobSpec(manifest, Options{})
	if err != nil {
		t.Fatalf("JobSpec failed: %v", err)
	}

	svc := jobruntime.NewJobService(store)
	job, err := svc.StartDetached(context.Background(), spec, AgentJobRunner(hub, manifest, "", Options{}))
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	finished := waitForRoleJobStatus(t, store, job.JobID, string(jobruntime.JobStatusTimedOut))
	if finished.Error == "" {
		t.Fatal("expected timeout error to be stored")
	}
	hub.mu.Lock()
	cancelled := hub.cancel
	hub.mu.Unlock()
	if cancelled == "" {
		t.Fatal("expected blocked agent run to be cancelled")
	}
}

func newRoleJobTestStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "rolejob.db"))
	if err != nil {
		t.Fatalf("storage.New failed: %v", err)
	}
	return store
}

func waitForRoleJobStatus(t *testing.T, store *storage.Store, jobID, want string) *storage.Job {
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
