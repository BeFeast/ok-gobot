package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/bot"
	"ok-gobot/internal/config"
	"ok-gobot/internal/hygiene"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/supervisor"
)

// mockDataProvider implements DataProvider for testing.
type mockDataProvider struct {
	jobs      []storage.Job
	job       *storage.Job
	events    []storage.JobEvent
	artifacts []storage.JobArtifact
	workers   []runtime.WorkerSnapshot
	cancelErr error
}

func (m *mockDataProvider) ListJobs(status string, limit int) ([]storage.Job, error) {
	if status == "" {
		return m.jobs, nil
	}
	var filtered []storage.Job
	for _, j := range m.jobs {
		if j.Status == status {
			filtered = append(filtered, j)
		}
	}
	return filtered, nil
}

func (m *mockDataProvider) GetJob(jobID string) (*storage.Job, error) {
	return m.job, nil
}

func (m *mockDataProvider) GetJobEvents(jobID string, limit int) ([]storage.JobEvent, error) {
	return m.events, nil
}

func (m *mockDataProvider) GetJobArtifacts(jobID string, limit int) ([]storage.JobArtifact, error) {
	return m.artifacts, nil
}

func (m *mockDataProvider) GetJobArtifact(artifactID int64) (*storage.JobArtifact, error) {
	for _, artifact := range m.artifacts {
		if artifact.ID == artifactID {
			copy := artifact
			return &copy, nil
		}
	}
	return nil, nil
}

func (m *mockDataProvider) CancelJob(jobID string) error {
	return m.cancelErr
}

func (m *mockDataProvider) WorkerSnapshots() []runtime.WorkerSnapshot {
	return m.workers
}

type mockHygieneProvider struct {
	report hygiene.Report
}

func (m mockHygieneProvider) GetHygieneReport(context.Context) (hygiene.Report, error) {
	return m.report, nil
}

func newTestServer(dp DataProvider) *APIServer {
	srv := NewAPIServer(config.APIConfig{
		Enabled: true,
		Port:    8080,
		APIKey:  "test-key",
	}, nil)
	srv.SetDataProvider(dp)
	return srv
}

func TestHandleJobsList(t *testing.T) {
	dp := &mockDataProvider{
		jobs: []storage.Job{
			{JobID: "job-1", Kind: "task", Status: "running", Description: "test job"},
			{JobID: "job-2", Kind: "task", Status: "succeeded", Description: "done job"},
		},
	}
	srv := newTestServer(dp)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	w := httptest.NewRecorder()
	srv.handleJobs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var result []storage.Job
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 jobs, got %d", len(result))
	}
}

func TestHandleJobsListWithStatusFilter(t *testing.T) {
	dp := &mockDataProvider{
		jobs: []storage.Job{
			{JobID: "job-1", Kind: "task", Status: "running", Description: "test job"},
			{JobID: "job-2", Kind: "task", Status: "succeeded", Description: "done job"},
		},
	}
	srv := newTestServer(dp)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs?status=running", nil)
	w := httptest.NewRecorder()
	srv.handleJobs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var result []storage.Job
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("Expected 1 running job, got %d", len(result))
	}
}

func TestHandleJobsEmptyList(t *testing.T) {
	dp := &mockDataProvider{}
	srv := newTestServer(dp)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	w := httptest.NewRecorder()
	srv.handleJobs(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if body != "[]\n" {
		t.Errorf("Expected empty JSON array, got %q", body)
	}
}

func TestHandleJobDetail(t *testing.T) {
	dp := &mockDataProvider{
		job: &storage.Job{
			JobID:       "job-1",
			Kind:        "task",
			Status:      "running",
			Description: "test job",
		},
		events: []storage.JobEvent{
			{ID: 1, JobID: "job-1", EventType: "created", Message: "test job"},
		},
		artifacts: []storage.JobArtifact{},
	}
	srv := newTestServer(dp)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs/job-1", nil)
	w := httptest.NewRecorder()
	srv.handleJobByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	if result["job"] == nil {
		t.Error("Expected job in response")
	}
	if result["events"] == nil {
		t.Error("Expected events in response")
	}
}

func TestHandleJobDetailSerializesSafeArtifacts(t *testing.T) {
	root := t.TempDir()
	screenshotPath := filepath.Join(root, "proof.png")
	if err := os.WriteFile(screenshotPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write screenshot: %v", err)
	}

	dp := &mockDataProvider{
		job: &storage.Job{JobID: "job-proof", Kind: "task", Status: "succeeded", Description: "proof job"},
		artifacts: []storage.JobArtifact{
			{ID: 1, JobID: "job-proof", Name: "Screenshot", ArtifactType: "screenshot", MimeType: "image/png", URI: screenshotPath, CreatedAt: "2026-04-30T10:00:00Z"},
			{ID: 2, JobID: "job-proof", Name: "PR", ArtifactType: "url", URI: "https://github.com/BeFeast/ok-gobot/pull/331"},
			{ID: 3, JobID: "job-proof", Name: "Report", ArtifactType: "text_report", MimeType: "text/markdown", Content: "tests passed"},
		},
	}
	srv := newTestServer(dp)
	srv.SetArtifactRoots([]string{root})

	req := httptest.NewRequest(http.MethodGet, "/api/jobs/job-proof", nil)
	w := httptest.NewRecorder()
	srv.handleJobByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var result struct {
		Artifacts []struct {
			Type    string `json:"type"`
			Label   string `json:"label"`
			Path    string `json:"path"`
			URL     string `json:"url"`
			Content string `json:"content"`
			Display struct {
				Kind string `json:"kind"`
				Safe bool   `json:"safe"`
				Href string `json:"href"`
			} `json:"display"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	if len(result.Artifacts) != 3 {
		t.Fatalf("artifact count = %d, want 3", len(result.Artifacts))
	}
	if got := result.Artifacts[0]; got.Type != "screenshot" || got.Label != "Screenshot" || got.Path != screenshotPath || got.Display.Kind != "image" || !got.Display.Safe || got.Display.Href != "/api/artifacts/1/content" {
		t.Fatalf("unexpected screenshot artifact: %+v", got)
	}
	if got := result.Artifacts[1]; got.URL == "" || got.Display.Kind != "url" || !got.Display.Safe {
		t.Fatalf("unexpected URL artifact: %+v", got)
	}
	if got := result.Artifacts[2]; got.Content != "tests passed" || got.Display.Kind != "text_report" || !got.Display.Safe {
		t.Fatalf("unexpected text report artifact: %+v", got)
	}
}

func TestHandleJobDetailRedactsUnsafeArtifactPath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, []byte("png"), 0o644); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}

	dp := &mockDataProvider{
		job:       &storage.Job{JobID: "job-proof", Kind: "task", Status: "succeeded", Description: "proof job"},
		artifacts: []storage.JobArtifact{{ID: 9, JobID: "job-proof", Name: "Outside", ArtifactType: "screenshot", MimeType: "image/png", URI: outside}},
	}
	srv := newTestServer(dp)
	srv.SetArtifactRoots([]string{root})

	req := httptest.NewRequest(http.MethodGet, "/api/jobs/job-proof", nil)
	w := httptest.NewRecorder()
	srv.handleJobByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var result struct {
		Artifacts []struct {
			Path    string `json:"path"`
			URI     string `json:"uri"`
			Display struct {
				Safe   bool   `json:"safe"`
				Reason string `json:"reason"`
				Href   string `json:"href"`
			} `json:"display"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	if len(result.Artifacts) != 1 {
		t.Fatalf("artifact count = %d, want 1", len(result.Artifacts))
	}
	artifact := result.Artifacts[0]
	if artifact.Path != "" || artifact.URI != "" || artifact.Display.Href != "" || artifact.Display.Safe || artifact.Display.Reason == "" {
		t.Fatalf("unsafe artifact path was exposed: %+v", artifact)
	}
}

func TestHandleArtifactContentPathSafety(t *testing.T) {
	root := t.TempDir()
	safePath := filepath.Join(root, "proof.txt")
	if err := os.WriteFile(safePath, []byte("safe proof"), 0o644); err != nil {
		t.Fatalf("write safe artifact: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside artifact: %v", err)
	}

	dp := &mockDataProvider{artifacts: []storage.JobArtifact{
		{ID: 1, JobID: "job-proof", Name: "safe", ArtifactType: "text_report", MimeType: "text/plain", URI: safePath},
		{ID: 2, JobID: "job-proof", Name: "unsafe", ArtifactType: "text_report", MimeType: "text/plain", URI: outside},
	}}
	srv := newTestServer(dp)
	srv.SetArtifactRoots([]string{root})

	w := httptest.NewRecorder()
	srv.handleArtifactContent(w, httptest.NewRequest(http.MethodGet, "/api/artifacts/1/content", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "safe proof") {
		t.Fatalf("expected safe artifact content, code=%d body=%q", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	srv.handleArtifactContent(w, httptest.NewRequest(http.MethodGet, "/api/artifacts/2/content", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected forbidden unsafe artifact content, got %d", w.Code)
	}
}

func TestHandleJobDetailNotFound(t *testing.T) {
	dp := &mockDataProvider{job: nil}
	srv := newTestServer(dp)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs/nonexistent", nil)
	w := httptest.NewRecorder()
	srv.handleJobByID(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("Expected 404, got %d", w.Code)
	}
}

func TestHandleJobCancel(t *testing.T) {
	dp := &mockDataProvider{}
	srv := newTestServer(dp)

	req := httptest.NewRequest(http.MethodPost, "/api/jobs/job-1/cancel", nil)
	w := httptest.NewRecorder()
	srv.handleJobByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	if result["success"] != true {
		t.Error("Expected success=true")
	}
}

func TestHandleWorkers(t *testing.T) {
	dp := &mockDataProvider{
		workers: []runtime.WorkerSnapshot{
			{SessionKey: "dm:123", Running: true, QueueDepth: 0},
			{SessionKey: "group:456", Running: false, QueueDepth: 2},
		},
	}
	srv := newTestServer(dp)

	req := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	w := httptest.NewRecorder()
	srv.handleWorkers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var result []runtime.WorkerSnapshot
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 workers, got %d", len(result))
	}
}

func TestHandleWorkersEmpty(t *testing.T) {
	dp := &mockDataProvider{}
	srv := newTestServer(dp)

	req := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	w := httptest.NewRecorder()
	srv.handleWorkers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if body != "[]\n" {
		t.Errorf("Expected empty JSON array, got %q", body)
	}
}

func TestHandleRoute(t *testing.T) {
	tests := []struct {
		name           string
		text           string
		expectedAction string
	}{
		{
			name:           "empty input returns clarification",
			text:           "",
			expectedAction: "clarification",
		},
		{
			name:           "simple message returns reply",
			text:           "hello",
			expectedAction: "reply",
		},
		{
			name:           "forced job prefix returns launch_job",
			text:           "job: investigate the CI pipeline",
			expectedAction: "launch_job",
		},
	}

	srv := newTestServer(&mockDataProvider{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(RouteRequest{Text: tt.text})
			req := httptest.NewRequest(http.MethodPost, "/api/route", bytes.NewReader(body))
			w := httptest.NewRecorder()
			srv.handleRoute(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("Expected 200, got %d", w.Code)
			}

			var result map[string]interface{}
			if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
				t.Fatalf("Failed to decode: %v", err)
			}
			if result["action"] != tt.expectedAction {
				t.Errorf("Expected action=%q, got %q", tt.expectedAction, result["action"])
			}
		})
	}
}

func TestHandleJobsNoDataProvider(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{
		Enabled: true,
		Port:    8080,
		APIKey:  "test-key",
	}, nil)
	// No SetDataProvider call

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	w := httptest.NewRecorder()
	srv.handleJobs(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("Expected 503, got %d", w.Code)
	}
}

func TestHandleJobsWrongMethod(t *testing.T) {
	srv := newTestServer(&mockDataProvider{})

	req := httptest.NewRequest(http.MethodPost, "/api/jobs", nil)
	w := httptest.NewRecorder()
	srv.handleJobs(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Expected 405, got %d", w.Code)
	}
}

func TestHandleJobDetailIncludesNewFields(t *testing.T) {
	dp := &mockDataProvider{
		job: &storage.Job{
			JobID:         "job-meta",
			Kind:          "task",
			Status:        "running",
			Description:   "test job",
			RoleName:      "researcher",
			ModelTier:     "premium",
			ToolCallCount: 5,
		},
		events:    []storage.JobEvent{},
		artifacts: []storage.JobArtifact{},
	}
	srv := newTestServer(dp)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs/job-meta", nil)
	w := httptest.NewRecorder()
	srv.handleJobByID(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "researcher") {
		t.Errorf("Expected 'researcher' in body: %s", body)
	}
	if !strings.Contains(body, "premium") {
		t.Errorf("Expected 'premium' in body: %s", body)
	}
}

func TestHandleMissionEstop(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{
		Enabled: true,
		Port:    8080,
		APIKey:  "test-key",
	}, nil)
	// Without bot, should return error
	req := httptest.NewRequest(http.MethodGet, "/api/mission/estop", nil)
	w := httptest.NewRecorder()
	srv.handleMissionEstop(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Expected 500 without bot, got %d", w.Code)
	}
}

func TestHandleMissionProviders(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{
		Enabled: true,
		Port:    8080,
		APIKey:  "test-key",
	}, nil)
	// Without bot, should return error
	req := httptest.NewRequest(http.MethodGet, "/api/mission/providers", nil)
	w := httptest.NewRecorder()
	srv.handleMissionProviders(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Expected 500 without bot, got %d", w.Code)
	}
}

func TestHandleMissionMemory(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{
		Enabled: true,
		Port:    8080,
		APIKey:  "test-key",
	}, nil)
	// Without bot, should return error
	req := httptest.NewRequest(http.MethodGet, "/api/mission/memory", nil)
	w := httptest.NewRecorder()
	srv.handleMissionMemory(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Expected 500 without bot, got %d", w.Code)
	}
}

func TestHandleMissionEvidence(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{
		Enabled: true,
		Port:    8080,
		APIKey:  "test-key",
	}, nil)
	// Without bot, should return error.
	req := httptest.NewRequest(http.MethodGet, "/api/mission/evidence?session_key=agent:test:main", nil)
	w := httptest.NewRecorder()
	srv.handleMissionEvidence(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("Expected 500 without bot, got %d", w.Code)
	}
}

func TestHandleMissionSupervisor(t *testing.T) {
	b := &bot.Bot{}
	b.SetSupervisorStatus(supervisor.Status{
		CurrentDecision: &supervisor.Decision{
			State:   supervisor.StateReadyForMerge,
			Subject: "issue-356",
		},
		LastSafeAction: &supervisor.ActionRecord{
			Subject: "issue-356",
			State:   supervisor.StateReadyForMerge,
			Action:  supervisor.Action{Kind: supervisor.ActionLabelReady},
		},
	})
	srv := NewAPIServer(config.APIConfig{Enabled: true, Port: 8080, APIKey: "test-key"}, b)

	req := httptest.NewRequest(http.MethodGet, "/api/mission/supervisor", nil)
	w := httptest.NewRecorder()
	srv.handleMissionSupervisor(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}
	var got supervisor.Status
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.CurrentDecision == nil || got.CurrentDecision.State != supervisor.StateReadyForMerge {
		t.Fatalf("unexpected supervisor status: %+v", got)
	}
}

func TestHandleMissionHygiene(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	srv := newTestServer(&mockDataProvider{})
	srv.SetHygieneProvider(mockHygieneProvider{report: hygiene.Report{
		GeneratedAt: now,
		Summary: hygiene.Summary{
			Status:          "attention_needed",
			TotalFindings:   1,
			SafeActionCount: 1,
			GeneratedAt:     now,
		},
		SafeActions: []hygiene.Finding{{
			ID:          hygiene.FindingDeadWorker,
			Severity:    hygiene.SeverityCritical,
			Title:       "dead worker",
			ActionGroup: hygiene.ActionSafe,
		}},
	}})

	req := httptest.NewRequest(http.MethodGet, "/api/mission/hygiene", nil)
	w := httptest.NewRecorder()
	srv.handleMissionHygiene(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"attention_needed", "dead_worker", "safe_actions"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
}

func TestHandleMissionSupervisor_WrongMethod(t *testing.T) {
	srv := newTestServer(&mockDataProvider{})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/supervisor", nil)
	w := httptest.NewRecorder()
	srv.handleMissionSupervisor(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Expected 405, got %d", w.Code)
	}
}

func TestHandleMissionEstop_WrongMethod(t *testing.T) {
	srv := newTestServer(&mockDataProvider{})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/estop", nil)
	w := httptest.NewRecorder()
	srv.handleMissionEstop(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Expected 405, got %d", w.Code)
	}
}

func TestHandleMissionProviders_WrongMethod(t *testing.T) {
	srv := newTestServer(&mockDataProvider{})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/providers", nil)
	w := httptest.NewRecorder()
	srv.handleMissionProviders(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Expected 405, got %d", w.Code)
	}
}

func TestHandleMissionMemory_WrongMethod(t *testing.T) {
	srv := newTestServer(&mockDataProvider{})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/memory", nil)
	w := httptest.NewRecorder()
	srv.handleMissionMemory(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Expected 405, got %d", w.Code)
	}
}

func TestHandleMissionEvidence_WrongMethod(t *testing.T) {
	srv := newTestServer(&mockDataProvider{})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/evidence", nil)
	w := httptest.NewRecorder()
	srv.handleMissionEvidence(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Expected 405, got %d", w.Code)
	}
}

func TestHandleMissionHygiene_WrongMethod(t *testing.T) {
	srv := newTestServer(&mockDataProvider{})
	req := httptest.NewRequest(http.MethodPost, "/api/mission/hygiene", nil)
	w := httptest.NewRecorder()
	srv.handleMissionHygiene(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Expected 405, got %d", w.Code)
	}
}
