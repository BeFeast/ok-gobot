package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/runtime"
)

func TestHandleJobsNoStore(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{APIKey: "k"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	w := httptest.NewRecorder()
	srv.handleJobs(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestHandleJobsMethodNotAllowed(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{APIKey: "k"}, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/jobs", nil)
	w := httptest.NewRecorder()
	srv.handleJobs(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestHandleWorkersNoHub(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{APIKey: "k"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	w := httptest.NewRecorder()
	srv.handleWorkers(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestHandleWorkersWithHub(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{APIKey: "k"}, nil)
	ctx := t.Context()
	hub := runtime.NewHub(ctx, 4)
	srv.SetRuntimeHub(hub)

	req := httptest.NewRequest(http.MethodGet, "/api/workers", nil)
	w := httptest.NewRecorder()
	srv.handleWorkers(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["count"] != float64(0) {
		t.Errorf("expected 0 workers, got %v", resp["count"])
	}
}

func TestHandleRoutesNoLog(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{APIKey: "k"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/routes", nil)
	w := httptest.NewRecorder()
	srv.handleRoutes(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestHandleRoutesWithLog(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{APIKey: "k"}, nil)
	rl := runtime.NewRouteLog(10)
	srv.SetRouteLog(rl)

	// Record a decision.
	rl.Record("session:1", "fix the bug in main.go", runtime.ChatRouteDecision{
		Action: runtime.ChatActionLaunchJob,
		Reason: "heavy_work_request",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/routes?limit=10", nil)
	w := httptest.NewRecorder()
	srv.handleRoutes(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if resp["count"] != float64(1) {
		t.Errorf("expected 1 route, got %v", resp["count"])
	}
}

func TestHandleJobByIDNotFound(t *testing.T) {
	srv := NewAPIServer(config.APIConfig{APIKey: "k"}, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/jobs/", nil)
	w := httptest.NewRecorder()
	srv.handleJobByID(w, req)

	// No store → service unavailable
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}

func TestParseIntParam(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/test?limit=25", nil)
	if v := parseIntParam(req, "limit", 50); v != 25 {
		t.Errorf("expected 25, got %d", v)
	}

	req = httptest.NewRequest(http.MethodGet, "/test", nil)
	if v := parseIntParam(req, "limit", 50); v != 50 {
		t.Errorf("expected default 50, got %d", v)
	}

	req = httptest.NewRequest(http.MethodGet, "/test?limit=abc", nil)
	if v := parseIntParam(req, "limit", 50); v != 50 {
		t.Errorf("expected default 50 for invalid param, got %d", v)
	}
}
