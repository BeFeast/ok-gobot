package cli

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

type missionControlSnapshot struct {
	GeneratedAt      string                `json:"generated_at"`
	TodayWindowStart string                `json:"today_window_start"`
	TodayWindowEnd   string                `json:"today_window_end"`
	Summary          missionControlSummary `json:"summary"`
	Roles            []missionControlRole  `json:"roles"`
	Schedules        []missionControlJob   `json:"schedules"`
	Runs             []missionControlRun   `json:"runs"`
}

type missionControlSummary struct {
	RoleCount        int `json:"role_count"`
	EnabledSchedules int `json:"enabled_schedules"`
	FailedRuns       int `json:"failed_runs"`
	DeliveredToday   int `json:"delivered_today"`
}

type missionControlRole struct {
	Name         string   `json:"name"`
	Kind         string   `json:"kind"`
	Model        string   `json:"model"`
	SoulPath     string   `json:"soul_path"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	ToolPolicy   string   `json:"tool_policy"`
}

type missionControlJob struct {
	ID             int64  `json:"id"`
	Expression     string `json:"expression"`
	Task           string `json:"task"`
	ChatID         int64  `json:"chat_id"`
	Type           string `json:"type"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Enabled        bool   `json:"enabled"`
	NextRun        string `json:"next_run,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type missionControlRun struct {
	ID               int64  `json:"id"`
	RunSlug          string `json:"run_slug"`
	Role             string `json:"role"`
	Task             string `json:"task"`
	Model            string `json:"model"`
	Thinking         string `json:"thinking"`
	ToolPolicy       string `json:"tool_policy"`
	WorkspaceRoot    string `json:"workspace_root"`
	DeliverBack      bool   `json:"deliver_back"`
	Status           string `json:"status"`
	ValuePreview     string `json:"value_preview,omitempty"`
	ErrorPreview     string `json:"error_preview,omitempty"`
	ParentSessionKey string `json:"parent_session_key"`
	SpawnedAt        string `json:"spawned_at"`
	CompletedAt      string `json:"completed_at,omitempty"`
}

var missionControlCronParser = cron.NewParser(cron.Second | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func loadMissionControlSnapshot(cfg *config.Config, store *storage.Store, now time.Time) (missionControlSnapshot, error) {
	windowStart, windowEnd := missionControlDayWindow(now)
	snapshot := missionControlSnapshot{
		GeneratedAt:      now.UTC().Format(time.RFC3339),
		TodayWindowStart: windowStart.Format(time.RFC3339),
		TodayWindowEnd:   windowEnd.Format(time.RFC3339),
		Roles:            loadMissionControlRoles(cfg),
	}
	if store == nil {
		return snapshot, errors.New("store not configured")
	}

	schedules, err := loadMissionControlSchedules(store, now)
	if err != nil {
		return snapshot, err
	}
	runs, err := loadMissionControlRuns(store, 100)
	if err != nil {
		return snapshot, err
	}

	snapshot.Schedules = schedules
	snapshot.Runs = runs
	summary, err := loadMissionControlSummary(store, len(snapshot.Roles), schedules, now)
	if err != nil {
		return snapshot, err
	}
	snapshot.Summary = summary
	return snapshot, nil
}

func loadMissionControlRoles(cfg *config.Config) []missionControlRole {
	defaultModel := strings.TrimSpace(cfg.AI.Model)
	roles := []missionControlRole{
		{
			Name:       "default",
			Kind:       "default",
			Model:      defaultModel,
			SoulPath:   cfg.GetSoulPath(),
			ToolPolicy: "All tools",
		},
	}

	seen := map[string]struct{}{
		"default": {},
	}

	for _, agentCfg := range cfg.Agents {
		name := strings.TrimSpace(agentCfg.Name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}

		model := strings.TrimSpace(agentCfg.Model)
		if model == "" {
			model = defaultModel
		}

		toolPolicy := "All tools"
		if len(agentCfg.AllowedTools) > 0 {
			toolPolicy = strings.Join(agentCfg.AllowedTools, ", ")
		}

		roles = append(roles, missionControlRole{
			Name:         name,
			Kind:         "agent",
			Model:        model,
			SoulPath:     agentCfg.SoulPath,
			AllowedTools: append([]string(nil), agentCfg.AllowedTools...),
			ToolPolicy:   toolPolicy,
		})
	}

	if len(roles) > 1 {
		namedRoles := roles[1:]
		sort.SliceStable(namedRoles, func(i, j int) bool {
			return namedRoles[i].Name < namedRoles[j].Name
		})
	}

	return roles
}

func loadMissionControlSchedules(store *storage.Store, now time.Time) ([]missionControlJob, error) {
	rows, err := store.DB().Query(`
		SELECT id, expression, task, chat_id, enabled, created_at,
		       COALESCE(type, 'llm'), COALESCE(timeout_seconds, 0)
		FROM cron_jobs
		ORDER BY created_at DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []missionControlJob
	for rows.Next() {
		var job missionControlJob
		var enabledInt int
		if err := rows.Scan(
			&job.ID,
			&job.Expression,
			&job.Task,
			&job.ChatID,
			&enabledInt,
			&job.CreatedAt,
			&job.Type,
			&job.TimeoutSeconds,
		); err != nil {
			return nil, err
		}
		job.Enabled = enabledInt != 0
		job.CreatedAt = formatMissionControlTime(job.CreatedAt)
		if job.Enabled {
			schedule, err := missionControlCronParser.Parse(job.Expression)
			if err == nil {
				job.NextRun = schedule.Next(now).UTC().Format(time.RFC3339)
			}
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(jobs, func(i, j int) bool {
		if jobs[i].Enabled != jobs[j].Enabled {
			return jobs[i].Enabled
		}

		iNext, iOK := parseMissionControlTime(jobs[i].NextRun)
		jNext, jOK := parseMissionControlTime(jobs[j].NextRun)
		if iOK && jOK && !iNext.Equal(jNext) {
			return iNext.Before(jNext)
		}
		if iOK != jOK {
			return iOK
		}

		return jobs[i].ID > jobs[j].ID
	})

	return jobs, nil
}

func loadMissionControlRuns(store *storage.Store, limit int) ([]missionControlRun, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := store.DB().Query(`
		SELECT id, run_slug, agent_id, task, model, thinking, tool_allowlist,
		       workspace_root, deliver_back, status, result, error,
		       parent_session_key, spawned_at, COALESCE(completed_at, '')
		FROM subagent_runs
		ORDER BY spawned_at DESC, id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []missionControlRun
	for rows.Next() {
		var run missionControlRun
		var toolAllowlist string
		var deliverBackInt int
		var result string
		var errText string
		if err := rows.Scan(
			&run.ID,
			&run.RunSlug,
			&run.Role,
			&run.Task,
			&run.Model,
			&run.Thinking,
			&toolAllowlist,
			&run.WorkspaceRoot,
			&deliverBackInt,
			&run.Status,
			&result,
			&errText,
			&run.ParentSessionKey,
			&run.SpawnedAt,
			&run.CompletedAt,
		); err != nil {
			return nil, err
		}

		run.DeliverBack = deliverBackInt != 0
		run.SpawnedAt = formatMissionControlTime(run.SpawnedAt)
		run.CompletedAt = formatMissionControlTime(run.CompletedAt)
		run.ToolPolicy = "All tools"
		if strings.TrimSpace(toolAllowlist) != "" {
			run.ToolPolicy = strings.Join(splitAndTrim(toolAllowlist), ", ")
		}
		run.ValuePreview = compactMissionControlText(result, 240)
		run.ErrorPreview = compactMissionControlText(errText, 220)
		runs = append(runs, run)
	}

	return runs, rows.Err()
}

func loadMissionControlSummary(store *storage.Store, roleCount int, schedules []missionControlJob, now time.Time) (missionControlSummary, error) {
	summary := missionControlSummary{
		RoleCount: roleCount,
	}

	for _, job := range schedules {
		if job.Enabled {
			summary.EnabledSchedules++
		}
	}

	windowStart, windowEnd := missionControlDayWindowUTC(now)
	row := store.DB().QueryRow(`
		SELECT
			COALESCE(SUM(CASE
				WHEN status = 'error' AND completed_at >= ? AND completed_at < ? THEN 1
				ELSE 0
			END), 0),
			COALESCE(SUM(CASE
				WHEN status = 'done' AND TRIM(COALESCE(result, '')) <> '' AND completed_at >= ? AND completed_at < ? THEN 1
				ELSE 0
			END), 0)
		FROM subagent_runs
	`, windowStart, windowEnd, windowStart, windowEnd)
	if err := row.Scan(&summary.FailedRuns, &summary.DeliveredToday); err != nil {
		return summary, err
	}

	return summary, nil
}

func compactMissionControlText(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	if limit <= 3 {
		return string(runes[:limit])
	}
	return string(runes[:limit-3]) + "..."
}

func splitAndTrim(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func missionControlDayWindow(now time.Time) (time.Time, time.Time) {
	nowLocal := now.In(time.Local)
	startLocal := time.Date(nowLocal.Year(), nowLocal.Month(), nowLocal.Day(), 0, 0, 0, 0, time.Local)
	endLocal := startLocal.Add(24 * time.Hour)
	return startLocal.UTC(), endLocal.UTC()
}

func missionControlDayWindowUTC(now time.Time) (string, string) {
	startUTC, endUTC := missionControlDayWindow(now)
	return startUTC.Format("2006-01-02 15:04:05"), endUTC.Format("2006-01-02 15:04:05")
}

func parseMissionControlTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}

	layouts := []struct {
		layout string
		utc    bool
	}{
		{layout: time.RFC3339Nano},
		{layout: time.RFC3339},
		{layout: "2006-01-02 15:04:05", utc: true},
		{layout: "2006-01-02T15:04:05", utc: true},
		{layout: "2006-01-02 15:04:05-07:00"},
	}

	for _, layout := range layouts {
		var (
			t   time.Time
			err error
		)
		if layout.utc {
			t, err = time.ParseInLocation(layout.layout, raw, time.UTC)
		} else {
			t, err = time.Parse(layout.layout, raw)
		}
		if err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

func formatMissionControlTime(raw string) string {
	if raw == "" {
		return ""
	}
	if t, ok := parseMissionControlTime(raw); ok {
		return t.UTC().Format(time.RFC3339)
	}
	return raw
}
