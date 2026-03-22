package control

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

// MissionProvider is an optional interface that StateProvider implementations
// can satisfy to expose mission-control data over the control server.
type MissionProvider interface {
	GetStore() *storage.Store
	GetAgentRegistry() *agent.AgentRegistry
	GetScheduler() tools.CronScheduler
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
			JobID       string `json:"job_id"`
			Kind        string `json:"kind"`
			Worker      string `json:"worker"`
			Description string `json:"description"`
			Status      string `json:"status"`
			Summary     string `json:"summary,omitempty"`
			Error       string `json:"error,omitempty"`
			Attempt     int    `json:"attempt"`
			MaxAttempts int    `json:"max_attempts"`
			CreatedAt   string `json:"created_at"`
			StartedAt   string `json:"started_at,omitempty"`
			CompletedAt string `json:"completed_at,omitempty"`
		}

		result := make([]runEntry, 0, len(jobs))
		for _, job := range jobs {
			if statusFilter != "" && job.Status != statusFilter {
				continue
			}
			result = append(result, runEntry{
				JobID:       job.JobID,
				Kind:        job.Kind,
				Worker:      job.Worker,
				Description: job.Description,
				Status:      job.Status,
				Summary:     job.Summary,
				Error:       job.Error,
				Attempt:     job.Attempt,
				MaxAttempts: job.MaxAttempts,
				CreatedAt:   job.CreatedAt,
				StartedAt:   job.StartedAt,
				CompletedAt: job.CompletedAt,
			})
		}
		writeJSON(w, result)
	}
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

		sessions, err := store.ListSessionsV2(100)
		if err != nil {
			writeJSONErr(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var totalTokens, totalMessages int
		for _, sess := range sessions {
			totalTokens += sess.TotalTokens
			totalMessages += sess.MessageCount
		}

		writeJSON(w, map[string]interface{}{
			"days":           stats,
			"total_tokens":   totalTokens,
			"total_messages": totalMessages,
			"session_count":  len(sessions),
		})
	}
}
