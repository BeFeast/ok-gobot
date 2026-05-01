package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"ok-gobot/internal/evidence"
	"ok-gobot/internal/hygiene"
	"ok-gobot/internal/storage"
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

func missionArtifactCounts(store *storage.Store, jobIDs []string) map[string]int {
	counts, err := store.CountJobArtifactsByJobIDs(jobIDs)
	if err != nil {
		return map[string]int{}
	}
	return counts
}

// handleMissionEstop returns the current emergency stop state.
func (s *APIServer) handleMissionEstop(w http.ResponseWriter, r *http.Request) {
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

	enabled, err := store.IsEmergencyStopEnabled()
	if err != nil {
		writeJSONError(w, "Failed to read estop state: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"estop_enabled": enabled,
	})
}

// handleMissionProviders returns the current AI provider info from bot status.
func (s *APIServer) handleMissionProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	status := s.bot.GetStatus()
	result := map[string]interface{}{
		"status": "ok",
	}

	if aiRaw, ok := status["ai"]; ok {
		switch ai := aiRaw.(type) {
		case map[string]interface{}:
			result["provider"] = ai["provider"]
			result["model"] = ai["model"]
		case map[string]string:
			result["provider"] = ai["provider"]
			result["model"] = ai["model"]
		}
	}

	writeJSON(w, result)
}

// handleMissionMemory returns memory health for Mission Control.
func (s *APIServer) handleMissionMemory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	status, err := s.bot.GetMemoryStatus(r.Context())
	if err != nil {
		writeJSONError(w, "Failed to read memory status: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, status)
}

// handleMissionEvidence returns the structured evidence timeline for a session.
func (s *APIServer) handleMissionEvidence(w http.ResponseWriter, r *http.Request) {
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

	sessionKey := strings.TrimSpace(r.URL.Query().Get("session_key"))
	if sessionKey == "" {
		writeJSONError(w, "session_key is required", http.StatusBadRequest)
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
		writeJSONError(w, "failed to list evidence", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]interface{}{
		"session_key": sessionKey,
		"events":      events,
		"markdown":    evidence.RenderMarkdown(events, evidence.RenderOptions{Limit: limit}),
	})
}

// handleMissionSupervisor returns the current supervisor decision and last safe action.
func (s *APIServer) handleMissionSupervisor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.bot == nil {
		writeJSONError(w, "Bot not available", http.StatusInternalServerError)
		return
	}

	writeJSON(w, s.bot.GetSupervisorStatus())
}

// handleMissionHygiene returns the latest read-only stale-state hygiene report.
func (s *APIServer) handleMissionHygiene(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.hygiene == nil {
		writeJSON(w, hygiene.Report{})
		return
	}
	report, err := s.hygiene.GetHygieneReport(r.Context())
	if err != nil {
		writeJSONError(w, "Failed to build hygiene report: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, report)
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

	// Aggregate session-level token totals across all sessions via SQL.
	totals, err := store.GetSessionTotals()
	if err != nil {
		writeJSONError(w, "Failed to get session totals: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]interface{}{
		"days":           stats,
		"total_tokens":   totals.TotalTokens,
		"total_messages": totals.TotalMessages,
		"session_count":  totals.SessionCount,
	})
}
