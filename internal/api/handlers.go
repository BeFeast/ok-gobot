package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"ok-gobot/internal/runtime"
)

// handleJobs returns a list of durable background jobs.
//
//	GET /api/jobs?status=running&limit=50
func (s *APIServer) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.data == nil {
		writeJSONError(w, "Data provider not configured", http.StatusServiceUnavailable)
		return
	}

	status := r.URL.Query().Get("status")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	jobs, err := s.data.ListJobs(status, limit)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if jobs == nil {
		// Ensure JSON [] instead of null.
		writeJSON(w, []interface{}{})
		return
	}
	writeJSON(w, jobs)
}

// handleJobByID handles per-job endpoints:
//
//	GET  /api/jobs/{id}        — job detail with events and artifacts
//	POST /api/jobs/{id}/cancel — request cancellation
func (s *APIServer) handleJobByID(w http.ResponseWriter, r *http.Request) {
	if s.data == nil {
		writeJSONError(w, "Data provider not configured", http.StatusServiceUnavailable)
		return
	}

	// Parse: /api/jobs/{id}  or  /api/jobs/{id}/cancel
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	if trimmed == "" {
		writeJSONError(w, "job ID is required", http.StatusBadRequest)
		return
	}

	parts := strings.SplitN(trimmed, "/", 2)
	jobID := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch {
	case action == "cancel" && r.Method == http.MethodPost:
		s.handleJobCancel(w, jobID)
	case action == "" && r.Method == http.MethodGet:
		s.handleJobDetail(w, jobID)
	default:
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleJobDetail returns a single job with its events and artifacts.
func (s *APIServer) handleJobDetail(w http.ResponseWriter, jobID string) {
	job, err := s.data.GetJob(jobID)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if job == nil {
		writeJSONError(w, "job not found", http.StatusNotFound)
		return
	}

	events, err := s.data.GetJobEvents(jobID, 200)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	artifacts, err := s.data.GetJobArtifacts(jobID, 100)
	if err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"job":       job,
		"events":    events,
		"artifacts": artifacts,
	})
}

// handleJobCancel requests cancellation of a background job.
func (s *APIServer) handleJobCancel(w http.ResponseWriter, jobID string) {
	if err := s.data.CancelJob(jobID); err != nil {
		writeJSONError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"success": true,
		"message": "Cancellation requested",
	})
}

// handleWorkers returns a snapshot of all active session workers.
//
//	GET /api/workers
func (s *APIServer) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.data == nil {
		writeJSONError(w, "Data provider not configured", http.StatusServiceUnavailable)
		return
	}

	snaps := s.data.WorkerSnapshots()
	if snaps == nil {
		snaps = []runtime.WorkerSnapshot{}
	}
	writeJSON(w, snaps)
}

// RouteRequest is the input for the router preview endpoint.
type RouteRequest struct {
	Text string `json:"text"`
}

// handleRoute previews a chat routing decision without executing it.
//
//	POST /api/route  {"text": "investigate the failing CI pipeline"}
func (s *APIServer) handleRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RouteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	decision := runtime.DecideChatRoute(req.Text)
	writeJSON(w, map[string]interface{}{
		"action":        string(decision.Action),
		"reason":        decision.Reason,
		"clarification": decision.Clarification,
	})
}
