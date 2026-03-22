package api

import (
	"net/http"
	"strconv"
	"time"
)

// handleMissionRoles returns all registered agent profiles.
func (s *APIServer) handleMissionRoles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	registry := s.bot.GetAgentRegistry()
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

// handleMissionSchedules returns all cron jobs with next-run times.
func (s *APIServer) handleMissionSchedules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	scheduler := s.bot.GetScheduler()
	if scheduler == nil {
		writeJSON(w, []interface{}{})
		return
	}

	jobs, err := scheduler.ListJobs()
	if err != nil {
		writeJSONError(w, "Failed to list schedules: "+err.Error(), http.StatusInternalServerError)
		return
	}

	type scheduleEntry struct {
		ID             int64  `json:"id"`
		Expression     string `json:"expression"`
		Task           string `json:"task"`
		Type           string `json:"type"`
		ChatID         int64  `json:"chat_id"`
		Enabled        bool   `json:"enabled"`
		TimeoutSeconds int    `json:"timeout_seconds"`
		NextRun        string `json:"next_run"`
		CreatedAt      string `json:"created_at"`
	}

	result := make([]scheduleEntry, 0, len(jobs))
	for _, job := range jobs {
		entry := scheduleEntry{
			ID:             job.ID,
			Expression:     job.Expression,
			Task:           job.Task,
			Type:           job.Type,
			ChatID:         job.ChatID,
			Enabled:        job.Enabled,
			TimeoutSeconds: job.TimeoutSeconds,
			CreatedAt:      job.CreatedAt,
		}
		if nextRun, err := scheduler.GetNextRun(job.ID); err == nil {
			entry.NextRun = nextRun.Format(time.RFC3339)
		}
		result = append(result, entry)
	}

	writeJSON(w, result)
}

// handleMissionRuns returns recent durable job runs.
func (s *APIServer) handleMissionRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	store := s.bot.GetStore()
	if store == nil {
		writeJSONError(w, "Store not available", http.StatusInternalServerError)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	// Optional status filter
	statusFilter := r.URL.Query().Get("status")

	jobs, err := store.ListJobs(limit)
	if err != nil {
		writeJSONError(w, "Failed to list runs: "+err.Error(), http.StatusInternalServerError)
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

// handleMissionStats returns daily aggregate statistics.
func (s *APIServer) handleMissionStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	store := s.bot.GetStore()
	if store == nil {
		writeJSONError(w, "Store not available", http.StatusInternalServerError)
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
		writeJSONError(w, "Failed to get stats: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Also get session-level token totals
	sessions, err := store.ListSessionsV2(100)
	if err != nil {
		writeJSONError(w, "Failed to list sessions: "+err.Error(), http.StatusInternalServerError)
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
