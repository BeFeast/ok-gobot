package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"ok-gobot/internal/storage"
)

// handleJobs lists durable jobs with optional status filter.
// GET /api/jobs?status=running&limit=50
func (s *APIServer) handleJobs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.store == nil {
		writeJSONError(w, "Job storage not available", http.StatusServiceUnavailable)
		return
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	limit := parseIntParam(r, "limit", 50)

	jobs, err := s.store.ListJobsByStatus(status, limit)
	if err != nil {
		writeJSONError(w, "Failed to list jobs: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if jobs == nil {
		jobs = []storage.Job{}
	}
	writeJSON(w, map[string]interface{}{
		"jobs":  jobs,
		"count": len(jobs),
	})
}

// handleJobByID dispatches sub-routes under /api/jobs/{id}.
//
//	GET  /api/jobs/{id}            → job detail + events
//	GET  /api/jobs/{id}/events     → job events
//	GET  /api/jobs/{id}/artifacts  → job artifacts
//	POST /api/jobs/{id}/cancel     → cancel job
func (s *APIServer) handleJobByID(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSONError(w, "Job storage not available", http.StatusServiceUnavailable)
		return
	}

	// Parse path: /api/jobs/{id}[/sub]
	path := strings.TrimPrefix(r.URL.Path, "/api/jobs/")
	parts := strings.SplitN(path, "/", 2)
	jobID := strings.TrimSpace(parts[0])
	if jobID == "" {
		writeJSONError(w, "job ID is required", http.StatusBadRequest)
		return
	}
	sub := ""
	if len(parts) > 1 {
		sub = parts[1]
	}

	switch sub {
	case "", "events":
		if r.Method != http.MethodGet {
			writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if sub == "events" {
			s.handleJobEvents(w, r, jobID)
		} else {
			s.handleJobDetail(w, r, jobID)
		}
	case "artifacts":
		if r.Method != http.MethodGet {
			writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleJobArtifacts(w, r, jobID)
	case "cancel":
		if r.Method != http.MethodPost {
			writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleJobCancel(w, r, jobID)
	default:
		writeJSONError(w, "unknown sub-resource: "+sub, http.StatusNotFound)
	}
}

func (s *APIServer) handleJobDetail(w http.ResponseWriter, r *http.Request, jobID string) {
	job, err := s.store.GetJob(jobID)
	if err != nil {
		writeJSONError(w, "Failed to get job: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if job == nil {
		writeJSONError(w, "Job not found", http.StatusNotFound)
		return
	}

	limit := parseIntParam(r, "event_limit", 100)
	events, err := s.store.ListJobEvents(jobID, limit)
	if err != nil {
		writeJSONError(w, "Failed to list events: "+err.Error(), http.StatusInternalServerError)
		return
	}
	artifacts, err := s.store.ListJobArtifacts(jobID, 100)
	if err != nil {
		writeJSONError(w, "Failed to list artifacts: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"job":       job,
		"events":    events,
		"artifacts": artifacts,
	})
}

func (s *APIServer) handleJobEvents(w http.ResponseWriter, r *http.Request, jobID string) {
	limit := parseIntParam(r, "limit", 100)
	events, err := s.store.ListJobEvents(jobID, limit)
	if err != nil {
		writeJSONError(w, "Failed to list events: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"job_id": jobID,
		"events": events,
		"count":  len(events),
	})
}

func (s *APIServer) handleJobArtifacts(w http.ResponseWriter, r *http.Request, jobID string) {
	limit := parseIntParam(r, "limit", 100)
	artifacts, err := s.store.ListJobArtifacts(jobID, limit)
	if err != nil {
		writeJSONError(w, "Failed to list artifacts: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"job_id":    jobID,
		"artifacts": artifacts,
		"count":     len(artifacts),
	})
}

func (s *APIServer) handleJobCancel(w http.ResponseWriter, _ *http.Request, jobID string) {
	if s.jobService == nil {
		writeJSONError(w, "Job service not available", http.StatusServiceUnavailable)
		return
	}
	if err := s.jobService.Cancel(jobID); err != nil {
		code := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			code = http.StatusNotFound
		}
		writeJSONError(w, "Failed to cancel job: "+err.Error(), code)
		return
	}
	writeJSON(w, map[string]interface{}{
		"success": true,
		"message": "Cancel requested for job " + jobID,
	})
}

// handleWorkers returns a snapshot of all active runtime session workers.
// GET /api/workers
func (s *APIServer) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.workerHub == nil {
		writeJSONError(w, "Runtime hub not available", http.StatusServiceUnavailable)
		return
	}

	workers := s.workerHub.WorkerSnapshots()
	writeJSON(w, map[string]interface{}{
		"workers": workers,
		"count":   len(workers),
	})
}

// handleRoutes returns recent chat-router decisions.
// GET /api/routes?limit=50
func (s *APIServer) handleRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.routeLog == nil {
		writeJSONError(w, "Route log not available", http.StatusServiceUnavailable)
		return
	}

	limit := parseIntParam(r, "limit", 50)
	records := s.routeLog.Recent(limit)

	writeJSON(w, map[string]interface{}{
		"routes": records,
		"count":  len(records),
	})
}

// writeJSONStatus writes a JSON response with the given status code.
func writeJSONStatus(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func parseIntParam(r *http.Request, name string, defaultVal int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return defaultVal
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return defaultVal
	}
	return v
}
