package control

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"ok-gobot/internal/agent"
	artifactview "ok-gobot/internal/artifacts"
	"ok-gobot/internal/evidence"
	"ok-gobot/internal/hygiene"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/supervisor"
	"ok-gobot/internal/tools"
)

// MissionProvider is an optional interface that StateProvider implementations
// can satisfy to expose mission-control data over the control server.
type MissionProvider interface {
	GetStore() *storage.Store
	GetAgentRegistry() *agent.AgentRegistry
	GetScheduler() tools.CronScheduler
	GetMemoryStatus(ctx context.Context) (memory.IndexStatus, error)
}

// MissionSupervisorProvider optionally exposes the supervisor recovery snapshot.
type MissionSupervisorProvider interface {
	GetSupervisorStatus() supervisor.Status
}

// MissionHygieneProvider optionally exposes the latest read-only stale-state
// hygiene report for Mission Control.
type MissionHygieneProvider interface {
	GetHygieneReport(ctx context.Context) (hygiene.Report, error)
}

// registerMissionRoutes adds mission-control HTTP routes to the mux if the
// state provider implements MissionProvider.
func (s *Server) registerMissionRoutes(mux *http.ServeMux) {
	mp, ok := s.state.(MissionProvider)
	if !ok {
		return
	}
	mux.HandleFunc("/api/mission/roles", s.corsWrap(missionRoles(mp)))
	mux.HandleFunc("/api/mission/schedules", s.corsWrap(missionSchedules(mp)))
	mux.HandleFunc("/api/mission/runs", s.corsWrap(missionRuns(mp)))
	mux.HandleFunc("/api/mission/stats", s.corsWrap(missionStats(mp)))
	mux.HandleFunc("/api/mission/estop", s.corsWrap(missionEstop(mp)))
	mux.HandleFunc("/api/mission/providers", s.corsWrap(missionProviders(s)))
	mux.HandleFunc("/api/mission/memory", s.corsWrap(missionMemory(mp)))
	mux.HandleFunc("/api/mission/evidence", s.corsWrap(missionEvidence(mp)))
	mux.HandleFunc("/api/mission/supervisor", s.corsWrap(missionSupervisor(mp)))
	mux.HandleFunc("/api/mission/hygiene", s.corsWrap(missionHygiene(mp)))
	mux.HandleFunc("/api/mission/artifacts/", s.corsWrap(missionArtifactContent(mp, s.artifactRoots)))
}

// corsWrap adds permissive CORS headers for loopback origins.
func (s *Server) corsWrap(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

func writeJSONErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg}) //nolint:errcheck
}

// ── handlers ─────────────────────────────────────────────────────────────────

func missionRoles(mp MissionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		registry := mp.GetAgentRegistry()
		if registry == nil {
			writeJSON(w, []interface{}{})
			return
		}

		type roleEntry struct {
			Name         string   `json:"name"`
			DisplayName  string   `json:"display_name"`
			Emoji        string   `json:"emoji"`
			Model        string   `json:"model"`
			AllowedTools []string `json:"allowed_tools"`
		}

		names := registry.List()
		roles := make([]roleEntry, 0, len(names))
		for _, name := range names {
			profile := registry.Get(name)
			if profile == nil {
				continue
			}
			entry := roleEntry{
				Name:         profile.Name,
				Model:        profile.Model,
				AllowedTools: profile.AllowedTools,
			}
			if profile.Personality != nil {
				entry.DisplayName = profile.Personality.GetName()
				entry.Emoji = profile.Personality.GetEmoji()
			}
			if entry.DisplayName == "" {
				entry.DisplayName = name
			}
			roles = append(roles, entry)
		}
		writeJSON(w, roles)
	}
}

func missionSchedules(mp MissionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		scheduler := mp.GetScheduler()
		if scheduler == nil {
			writeJSON(w, []interface{}{})
			return
		}

		jobs, err := scheduler.ListJobs()
		if err != nil {
			writeJSONErr(w, err.Error(), http.StatusInternalServerError)
			return
		}

		type entry struct {
			ID             int64  `json:"id"`
			Expression     string `json:"expression"`
			Task           string `json:"task"`
			Type           string `json:"type"`
			ChatID         int64  `json:"chat_id"`
			TimeoutSeconds int    `json:"timeout_seconds"`
			NextRun        string `json:"next_run"`
			CreatedAt      string `json:"created_at"`
		}

		out := make([]entry, 0, len(jobs))
		for _, j := range jobs {
			e := entry{
				ID:             j.ID,
				Expression:     j.Expression,
				Task:           j.Task,
				Type:           j.Type,
				ChatID:         j.ChatID,
				TimeoutSeconds: j.TimeoutSeconds,
				CreatedAt:      j.CreatedAt,
			}
			if nr, err := scheduler.GetNextRun(j.ID); err == nil {
				e.NextRun = nr.Format(time.RFC3339)
			}
			out = append(out, e)
		}
		writeJSON(w, out)
	}
}

func missionRuns(mp MissionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		store := mp.GetStore()
		if store == nil {
			writeJSONErr(w, "store unavailable", http.StatusInternalServerError)
			return
		}

		limit := 50
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		statusFilter := r.URL.Query().Get("status")

		jobs, err := store.ListJobs(limit)
		if err != nil {
			writeJSONErr(w, err.Error(), http.StatusInternalServerError)
			return
		}

		type runEntry struct {
			JobID         string `json:"job_id"`
			Kind          string `json:"kind"`
			Worker        string `json:"worker"`
			Description   string `json:"description"`
			Status        string `json:"status"`
			Summary       string `json:"summary,omitempty"`
			Error         string `json:"error,omitempty"`
			RoleName      string `json:"role_name,omitempty"`
			ModelTier     string `json:"model_tier,omitempty"`
			ToolCallCount int    `json:"tool_call_count,omitempty"`
			ArtifactCount int    `json:"artifact_count,omitempty"`
			Attempt       int    `json:"attempt"`
			MaxAttempts   int    `json:"max_attempts"`
			CreatedAt     string `json:"created_at"`
			StartedAt     string `json:"started_at,omitempty"`
			CompletedAt   string `json:"completed_at,omitempty"`
		}

		filteredJobs := make([]storage.Job, 0, len(jobs))
		jobIDs := make([]string, 0, len(jobs))
		for _, job := range jobs {
			if statusFilter != "" && job.Status != statusFilter {
				continue
			}
			filteredJobs = append(filteredJobs, job)
			jobIDs = append(jobIDs, job.JobID)
		}
		artifactCounts := missionArtifactCounts(store, jobIDs)

		result := make([]runEntry, 0, len(filteredJobs))
		for _, job := range filteredJobs {
			result = append(result, runEntry{
				JobID:         job.JobID,
				Kind:          job.Kind,
				Worker:        job.Worker,
				Description:   job.Description,
				Status:        job.Status,
				Summary:       job.Summary,
				Error:         job.Error,
				RoleName:      job.RoleName,
				ModelTier:     job.ModelTier,
				ToolCallCount: job.ToolCallCount,
				ArtifactCount: artifactCounts[job.JobID],
				Attempt:       job.Attempt,
				MaxAttempts:   job.MaxAttempts,
				CreatedAt:     job.CreatedAt,
				StartedAt:     job.StartedAt,
				CompletedAt:   job.CompletedAt,
			})
		}
		writeJSON(w, result)
	}
}

func missionArtifactContent(mp MissionProvider, rootsFn func() []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		store := mp.GetStore()
		if store == nil {
			writeJSONErr(w, "store unavailable", http.StatusInternalServerError)
			return
		}

		artifactID, ok := parseMissionArtifactContentPath(r.URL.Path)
		if !ok {
			writeJSONErr(w, "artifact ID is required", http.StatusBadRequest)
			return
		}

		artifact, err := store.GetJobArtifact(artifactID)
		if err != nil {
			writeJSONErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if artifact == nil {
			writeJSONErr(w, "artifact not found", http.StatusNotFound)
			return
		}

		path, err := artifactview.ContentPath(*artifact, rootsFn())
		if err != nil {
			if os.IsNotExist(err) {
				writeJSONErr(w, "artifact file not found", http.StatusNotFound)
				return
			}
			writeJSONErr(w, err.Error(), http.StatusForbidden)
			return
		}
		if artifact.MimeType != "" {
			w.Header().Set("Content-Type", artifact.MimeType)
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, path)
	}
}

func parseMissionArtifactContentPath(path string) (int64, bool) {
	const prefix = "/api/mission/artifacts/"
	trimmed := strings.TrimPrefix(path, prefix)
	if trimmed == path || !strings.HasSuffix(trimmed, "/content") {
		return 0, false
	}
	idPart := strings.TrimSuffix(trimmed, "/content")
	if idPart == "" || strings.Contains(idPart, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(idPart, 10, 64)
	return id, err == nil && id > 0
}

func missionArtifactCounts(store *storage.Store, jobIDs []string) map[string]int {
	counts, err := store.CountJobArtifactsByJobIDs(jobIDs)
	if err != nil {
		return map[string]int{}
	}
	return counts
}

func missionStats(mp MissionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		store := mp.GetStore()
		if store == nil {
			writeJSONErr(w, "store unavailable", http.StatusInternalServerError)
			return
		}

		days := 7
		if v := r.URL.Query().Get("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 90 {
				days = n
			}
		}

		stats, err := store.GetDailyStats(days)
		if err != nil {
			writeJSONErr(w, err.Error(), http.StatusInternalServerError)
			return
		}

		totals, err := store.GetSessionTotals()
		if err != nil {
			writeJSONErr(w, err.Error(), http.StatusInternalServerError)
			return
		}

		writeJSON(w, map[string]interface{}{
			"days":           stats,
			"total_tokens":   totals.TotalTokens,
			"total_messages": totals.TotalMessages,
			"session_count":  totals.SessionCount,
		})
	}
}

func missionEstop(mp MissionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		store := mp.GetStore()
		if store == nil {
			writeJSONErr(w, "store unavailable", http.StatusInternalServerError)
			return
		}
		enabled, err := store.IsEmergencyStopEnabled()
		if err != nil {
			writeJSONErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{"estop_enabled": enabled})
	}
}

func missionProviders(srv *Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status := srv.state.GetStatus()
		result := map[string]interface{}{"status": "ok"}
		if aiRaw, ok := status["ai"]; ok {
			switch ai := aiRaw.(type) {
			case map[string]interface{}:
				result["provider"] = ai["provider"]
				result["model"] = ai["model"]
				result["backend"] = ai["backend"]
				result["model_tier"] = ai["model_tier"]
				result["effort"] = ai["effort"]
				if health, ok := ai["health"]; ok {
					result["health"] = health
				}
			case map[string]string:
				result["provider"] = ai["provider"]
				result["model"] = ai["model"]
			}
		}
		writeJSON(w, result)
	}
}

func missionMemory(mp MissionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		status, err := mp.GetMemoryStatus(r.Context())
		if err != nil {
			writeJSONErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, status)
	}
}

func missionEvidence(mp MissionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		store := mp.GetStore()
		if store == nil {
			writeJSONErr(w, "store unavailable", http.StatusInternalServerError)
			return
		}
		sessionKey := strings.TrimSpace(r.URL.Query().Get("session_key"))
		if sessionKey == "" {
			writeJSONErr(w, "session_key is required", http.StatusBadRequest)
			return
		}
		limit := 25
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		events, err := store.ListEvidenceEvents(sessionKey, limit)
		if err != nil {
			writeJSONErr(w, "failed to list evidence", http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]interface{}{
			"session_key": sessionKey,
			"events":      events,
			"markdown":    evidence.RenderMarkdown(events, evidence.RenderOptions{Limit: limit}),
		})
	}
}

func missionSupervisor(mp MissionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		provider, ok := mp.(MissionSupervisorProvider)
		if !ok {
			writeJSON(w, supervisor.Status{})
			return
		}
		writeJSON(w, provider.GetSupervisorStatus())
	}
}

func missionHygiene(mp MissionProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeJSONErr(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		provider, ok := mp.(MissionHygieneProvider)
		if !ok {
			writeJSON(w, hygiene.Report{})
			return
		}
		report, err := provider.GetHygieneReport(r.Context())
		if err != nil {
			writeJSONErr(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, report)
	}
}
