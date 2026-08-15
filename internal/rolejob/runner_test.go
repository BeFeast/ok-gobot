package rolejob

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/config"
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

func TestAgentJobRunnerPersistsFrontendVerifyArtifacts(t *testing.T) {
	t.Parallel()

	store := newRoleJobTestStore(t)
	defer store.Close() //nolint:errcheck

	root := t.TempDir()
	shotPath := filepath.Join(root, "proof.png")
	if err := os.WriteFile(shotPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}
	shotURI := (&url.URL{Scheme: "file", Path: shotPath}).String()
	fallbackPath := filepath.Join(root, "fallback-proof.png")
	targetURL := "http://127.0.0.1:5173"
	textReport := "frontend_verify passed for local demo"
	fullOutput := `{"match":true,"status":"passed","url":"` + targetURL + `","text_report":"` + textReport + `","feedback":"` + strings.Repeat("verbose ", 60) + `","screenshot_uri":"` + shotURI + `","screenshot_path":"` + fallbackPath + `"}`
	truncatedOutput := fullOutput[:300] + "…"
	frontendVerifyEvent := agent.ToolEvent{
		ToolName:   "frontend_verify",
		Type:       agent.ToolEventFinished,
		Output:     truncatedOutput,
		FullOutput: fullOutput,
	}
	extracted := extractToolArtifacts(frontendVerifyEvent)
	if len(extracted) == 0 || extracted[0].Type != jobruntime.JobArtifactTypeScreenshot || extracted[0].URI != shotURI {
		t.Fatalf("frontend_verify screenshot artifact did not prefer screenshot_uri: %+v", extracted)
	}

	manifest := &role.Manifest{Name: "prototype", Prompt: "Build and verify UI", Worker: "standard"}
	hub := &fakeAgentHub{
		content: "verified",
		events:  []agent.ToolEvent{frontendVerifyEvent},
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
	foundScreenshot := false
	foundURL := false
	foundReport := false
	for _, artifact := range artifacts {
		if artifact.ArtifactType == jobruntime.JobArtifactTypeScreenshot {
			foundScreenshot = true
			// Persisted local file:// artifacts are stored by their validated safe path.
			if artifact.URI != shotPath || artifact.Content != "" {
				t.Fatalf("unexpected screenshot artifact: %+v", artifact)
			}
		}
		if artifact.ArtifactType == jobruntime.JobArtifactTypeURL && artifact.URI == targetURL {
			foundURL = true
		}
		if artifact.ArtifactType == jobruntime.JobArtifactTypeTextReport && strings.Contains(artifact.Content, textReport) {
			foundReport = true
		}
	}
	if !foundScreenshot {
		t.Fatalf("missing screenshot artifact: %+v", artifacts)
	}
	if !foundURL {
		t.Fatalf("missing frontend URL artifact: %+v", artifacts)
	}
	if !foundReport {
		t.Fatalf("missing frontend text_report artifact: %+v", artifacts)
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

func TestAgentJobRunnerAppliesCostTierUnderManifestFields(t *testing.T) {
	t.Parallel()

	store := newRoleJobTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "dm:42"
	if err := store.SaveSessionRoute(storage.SessionRoute{SessionKey: routeKey, Channel: "telegram", ChatID: 42}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	selector, err := SelectorFromConfig(config.RuntimeConfig{
		CostTiers: map[string]config.CostTierEntry{
			"cheap": {Model: "gpt-5.6-luna", Thinking: "low", MaxToolCalls: 30},
		},
	})
	if err != nil {
		t.Fatalf("SelectorFromConfig: %v", err)
	}

	// Manifest pins its own tool-call budget but no model: the tier fills
	// model+thinking, the manifest budget must survive.
	manifest := &role.Manifest{
		Name:         "monitor",
		Prompt:       "Check the fleet.",
		Worker:       "cheap",
		MaxToolCalls: 7,
	}
	hub := &fakeAgentHub{content: "ok"}
	opts := Options{SessionKey: routeKey, DeliverySessionKey: routeKey, ChatID: 42, Selector: selector}
	spec, err := JobSpec(manifest, opts)
	if err != nil {
		t.Fatalf("JobSpec failed: %v", err)
	}

	svc := jobruntime.NewJobService(store)
	if _, err := svc.StartDetached(context.Background(), spec, AgentJobRunner(hub, manifest, "", opts)); err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}
	waitForRequests(t, hub, 1)

	req := hub.firstRequest(t)
	if req.Job == nil {
		t.Fatal("expected a delegated job on the run request")
	}
	if req.Job.Model != "gpt-5.6-luna" || req.Job.Thinking != "low" {
		t.Fatalf("job model/thinking = %q/%q, want tier fill", req.Job.Model, req.Job.Thinking)
	}
	if req.Job.MaxToolCalls != 7 {
		t.Fatalf("MaxToolCalls = %d, manifest budget must win over the tier", req.Job.MaxToolCalls)
	}
}

func TestAgentJobRunnerManifestModelWinsOverTier(t *testing.T) {
	t.Parallel()

	store := newRoleJobTestStore(t)
	defer store.Close() //nolint:errcheck

	const routeKey = "dm:43"
	if err := store.SaveSessionRoute(storage.SessionRoute{SessionKey: routeKey, Channel: "telegram", ChatID: 43}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	selector, err := SelectorFromConfig(config.RuntimeConfig{
		CostTiers: map[string]config.CostTierEntry{
			"premium": {Model: "gpt-5.6-sol", Thinking: "high"},
		},
	})
	if err != nil {
		t.Fatalf("SelectorFromConfig: %v", err)
	}

	manifest := &role.Manifest{
		Name:   "researcher",
		Prompt: "Research.",
		Worker: "premium",
		Model:  "claude-opus-4-5-20251101",
	}
	hub := &fakeAgentHub{content: "ok"}
	opts := Options{SessionKey: routeKey, DeliverySessionKey: routeKey, ChatID: 43, Selector: selector}
	spec, err := JobSpec(manifest, opts)
	if err != nil {
		t.Fatalf("JobSpec failed: %v", err)
	}

	svc := jobruntime.NewJobService(store)
	if _, err := svc.StartDetached(context.Background(), spec, AgentJobRunner(hub, manifest, "", opts)); err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}
	waitForRequests(t, hub, 1)

	req := hub.firstRequest(t)
	if req.Job == nil || req.Job.Model != "claude-opus-4-5-20251101" {
		t.Fatalf("manifest model must win over the tier, got %+v", req.Job)
	}
	if req.Job.Thinking != "high" {
		t.Fatalf("Thinking = %q, tier must fill the empty field", req.Job.Thinking)
	}
}

func waitForRequests(t *testing.T, hub *fakeAgentHub, want int) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		hub.mu.Lock()
		n := len(hub.requests)
		hub.mu.Unlock()
		if n >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %d hub request(s)", want)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
