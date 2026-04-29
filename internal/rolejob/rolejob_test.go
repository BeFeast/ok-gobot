package rolejob

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/delegation"
	"ok-gobot/internal/role"
	"ok-gobot/internal/runtime"
)

type fakeHub struct {
	req agent.RunRequest
	ev  agent.RunEvent
}

func (h *fakeHub) Submit(req agent.RunRequest) <-chan agent.RunEvent {
	h.req = req
	ch := make(chan agent.RunEvent, 1)
	ch <- h.ev
	close(ch)
	return ch
}

func TestRunWithHubCallsRuntimeHubAndBuildsArtifacts(t *testing.T) {
	dir := t.TempDir()
	image := filepath.Join(dir, "proof.png")
	if err := os.WriteFile(image, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &role.Manifest{
		Name:         "prototype-builder",
		Prompt:       "Build the thing.",
		Worker:       "premium",
		Tools:        []string{"file", "frontend_verify"},
		MaxToolCalls: 7,
		MaxDuration:  2 * time.Minute,
		MemoryPolicy: delegation.MemoryPolicyReadOnly,
	}
	hub := &fakeHub{ev: agent.RunEvent{
		Type: agent.RunEventDone,
		Result: &agent.AgentResponse{
			Message: "Done.\nScreenshot: " + image + "\nPreview: http://127.0.0.1:5173",
		},
	}}

	result, err := RunWithHub(context.Background(), hub, m, "blue rocket", RunOptions{
		SessionKey: "role:test",
		ChatID:     42,
	})
	if err != nil {
		t.Fatalf("RunWithHub error = %v", err)
	}
	if hub.req.SessionKey != "role:test" {
		t.Fatalf("SessionKey = %q", hub.req.SessionKey)
	}
	if hub.req.ChatID != 42 {
		t.Fatalf("ChatID = %d", hub.req.ChatID)
	}
	if hub.req.Job == nil {
		t.Fatal("expected delegation job")
	}
	if hub.req.Job.MaxToolCalls != 7 {
		t.Fatalf("MaxToolCalls = %d", hub.req.Job.MaxToolCalls)
	}
	if !strings.Contains(hub.req.Content, "blue rocket") {
		t.Fatalf("input missing from prompt: %q", hub.req.Content)
	}
	if !strings.Contains(result.Summary, "Done.") {
		t.Fatalf("summary = %q", result.Summary)
	}
	if !hasArtifact(result.Artifacts, runtime.JobArtifactTypeScreenshot) {
		t.Fatalf("expected screenshot artifact: %#v", result.Artifacts)
	}
	if !hasArtifact(result.Artifacts, runtime.JobArtifactTypeURL) {
		t.Fatalf("expected URL artifact: %#v", result.Artifacts)
	}
	if !hasArtifact(result.Artifacts, runtime.JobArtifactTypeTextReport) {
		t.Fatalf("expected text_report artifact: %#v", result.Artifacts)
	}
}

func TestBuildSpecUsesRoleBudgetMetadata(t *testing.T) {
	m := &role.Manifest{
		Name:         "researcher",
		Worker:       "standard",
		MaxToolCalls: 3,
		MaxDuration:  90 * time.Second,
	}
	spec := BuildSpec(m, "check releases", "role:1", "", "premium")
	if spec.Kind != "role" {
		t.Fatalf("Kind = %q", spec.Kind)
	}
	if spec.Worker != "premium" {
		t.Fatalf("Worker = %q", spec.Worker)
	}
	if spec.RoleName != "researcher" {
		t.Fatalf("RoleName = %q", spec.RoleName)
	}
	if spec.ModelTier != "premium" {
		t.Fatalf("ModelTier = %q", spec.ModelTier)
	}
	if spec.Timeout != 90*time.Second {
		t.Fatalf("Timeout = %s", spec.Timeout)
	}
	if spec.MaxToolCalls != 3 {
		t.Fatalf("MaxToolCalls = %d", spec.MaxToolCalls)
	}
}

func hasArtifact(artifacts []runtime.JobArtifactSpec, typ string) bool {
	for _, a := range artifacts {
		if a.Type == typ {
			return true
		}
	}
	return false
}
