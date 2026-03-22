package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
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

func (m *mockDataProvider) CancelJob(jobID string) error {
	return m.cancelErr
}

func (m *mockDataProvider) WorkerSnapshots() []runtime.WorkerSnapshot {
	return m.workers
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
