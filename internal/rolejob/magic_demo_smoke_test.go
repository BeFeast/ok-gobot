package rolejob

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/role"
	jobruntime "ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

// TestMagicDemoSmoke_ProvesEndToEndControlPath exercises the same control path
// the Magic Demo walkthrough drives from Telegram (/role_run → /job →
// /skill_suggest), but with fakes for the agent runtime so it can run as part
// of `go test ./...` without a Telegram client, a real LLM, or a real browser.
//
// The harness asserts the four things the Magic Demo doc promises:
//
//  1. The role job starts and reaches a terminal `succeeded` status.
//  2. The runner produces a bounded summary that surfaces the worker result.
//  3. Proof artifacts (screenshot, URL, text report) persist to durable
//     storage and stay rooted under the configured artifact root.
//  4. A successful job can be distilled into a reviewable skill draft without
//     installing it.
func TestMagicDemoSmoke_ProvesEndToEndControlPath(t *testing.T) {
	t.Parallel()

	store := newRoleJobTestStore(t)
	defer store.Close() //nolint:errcheck

	const adminChatID = int64(99)
	const sessionKey = "dm:99"
	if err := store.SaveSessionRoute(storage.SessionRoute{SessionKey: sessionKey, Channel: "telegram", ChatID: adminChatID}); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	artifactRoot := t.TempDir()
	screenshotPath := filepath.Join(artifactRoot, "rocket-launch.png")
	if err := os.WriteFile(screenshotPath, []byte("\x89PNG fake"), 0o644); err != nil {
		t.Fatalf("write proof screenshot: %v", err)
	}
	screenshotURI := (&url.URL{Scheme: "file", Path: screenshotPath}).String()

	const previewURL = "http://127.0.0.1:5173"
	const textReport = "frontend_verify: rocket simulator renders blue 3D launchpad as briefed"
	frontendVerifyPayload := `{` +
		`"match":true,` +
		`"status":"passed",` +
		`"url":"` + previewURL + `",` +
		`"text_report":"` + textReport + `",` +
		`"screenshot_uri":"` + screenshotURI + `"` +
		`}`
	frontendVerifyEvent := agent.ToolEvent{
		ToolName:   "frontend_verify",
		Type:       agent.ToolEventFinished,
		Output:     frontendVerifyPayload,
		FullOutput: frontendVerifyPayload,
	}

	const finalSummary = "Built blue 3D rocket launch simulator at ./prototype.\n" +
		"Preview: http://127.0.0.1:5173\n" +
		"Verified visually with frontend_verify."

	manifest := &role.Manifest{
		Name:         "prototype-builder",
		Prompt:       "Build the requested frontend prototype and verify it visually.",
		Worker:       "standard",
		Tools:        []string{"local", "file", "patch", "frontend_verify"},
		MaxToolCalls: 12,
		MaxDuration:  5 * time.Minute,
	}
	const brief = "Build a blue 3D rocket launch simulator"

	hub := &fakeAgentHub{
		content: finalSummary,
		events:  []agent.ToolEvent{frontendVerifyEvent},
	}
	opts := Options{
		SessionKey:         sessionKey,
		DeliverySessionKey: sessionKey,
		ChatID:             adminChatID,
		ArtifactRoots:      []string{artifactRoot},
	}

	spec, err := JobSpec(manifest, opts)
	if err != nil {
		t.Fatalf("JobSpec failed: %v", err)
	}
	if spec.RoleName != "prototype-builder" {
		t.Fatalf("spec.RoleName = %q, want prototype-builder", spec.RoleName)
	}

	svc := jobruntime.NewJobService(store)
	svc.SetArtifactRoots([]string{artifactRoot})

	job, err := svc.StartDetached(context.Background(), spec, AgentJobRunner(hub, manifest, brief, opts))
	if err != nil {
		t.Fatalf("StartDetached failed: %v", err)
	}

	// 1. Job reaches a terminal succeeded status, mirroring the ✅ icon shown
	//    by /job in the demo doc.
	finished := waitForRoleJobStatus(t, store, job.JobID, string(jobruntime.JobStatusSucceeded))
	if finished.Status != string(jobruntime.JobStatusSucceeded) {
		t.Fatalf("final status = %q, want succeeded", finished.Status)
	}

	// 2. The summary stored on the durable job is the worker's real output, not
	//    a stub. This is what the Telegram final notification surfaces.
	if !strings.Contains(finished.Summary, "Built blue 3D rocket launch simulator") {
		t.Fatalf("Summary = %q, want worker result", finished.Summary)
	}
	if finished.RoleName != "prototype-builder" || finished.Worker != "standard" {
		t.Fatalf("Job metadata = (role=%q, worker=%q), want (prototype-builder, standard)", finished.RoleName, finished.Worker)
	}

	// The runner submitted exactly one agent request with both the manifest
	// prompt and the operator brief — the same payload /role_run constructs.
	req := hub.firstRequest(t)
	if !strings.Contains(req.Content, manifest.Prompt) || !strings.Contains(req.Content, "User input: "+brief) {
		t.Fatalf("agent request content = %q, want manifest prompt + brief", req.Content)
	}
	if string(req.SessionKey) == sessionKey {
		t.Fatalf("runtime session key should be isolated from delivery session key %q", sessionKey)
	}

	// 3. Proof artifacts persist to storage and stay within the configured
	//    artifact root. /job <id> serializes these for the operator.
	artifacts, err := store.ListJobArtifacts(job.JobID, 10)
	if err != nil {
		t.Fatalf("ListJobArtifacts failed: %v", err)
	}

	var (
		gotScreenshot bool
		gotURL        bool
		gotReport     bool
	)
	for _, a := range artifacts {
		switch a.ArtifactType {
		case jobruntime.JobArtifactTypeScreenshot:
			gotScreenshot = true
			if a.URI != screenshotPath {
				t.Fatalf("screenshot artifact URI = %q, want safe path %q", a.URI, screenshotPath)
			}
		case jobruntime.JobArtifactTypeURL:
			if a.URI == previewURL {
				gotURL = true
			}
		case jobruntime.JobArtifactTypeTextReport:
			if strings.Contains(a.Content, textReport) {
				gotReport = true
			}
		}
	}
	if !gotScreenshot || !gotURL || !gotReport {
		t.Fatalf("missing one of (screenshot=%t url=%t report=%t): %+v", gotScreenshot, gotURL, gotReport, artifacts)
	}

	// Lifecycle events expose the runner's `running role …` progress message
	// which /job replays as the latest evidence line.
	events, err := store.ListJobEvents(job.JobID, 20)
	if err != nil {
		t.Fatalf("ListJobEvents failed: %v", err)
	}
	sawProgress := false
	for _, e := range events {
		if e.EventType == string(jobruntime.JobEventProgress) && strings.Contains(e.Message, "running role prototype-builder") {
			sawProgress = true
			break
		}
	}
	if !sawProgress {
		t.Fatalf("missing role progress event: %+v", events)
	}

	// 4. /skill_suggest turns the succeeded job into a reviewable draft that is
	//    audited but not installed.
	soul := t.TempDir()
	suggestion, err := bootstrap.SuggestSkillFromJob(soul, store, finished.JobID)
	if err != nil {
		t.Fatalf("SuggestSkillFromJob failed: %v", err)
	}
	if suggestion == nil {
		t.Fatal("SuggestSkillFromJob returned no suggestion")
	}
	if suggestion.Unsafe {
		t.Fatalf("draft unexpectedly flagged unsafe: %+v", suggestion.AuditFindings)
	}
	if _, err := os.Stat(suggestion.SkillFile); err != nil {
		t.Fatalf("draft SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(soul, "skills")); !os.IsNotExist(err) {
		t.Fatalf("skill_suggest must not install into <soul>/skills: %v", err)
	}
}
