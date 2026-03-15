package cli

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func TestLoadMissionControlSnapshot(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("MissionControlTest", 2*60*60)
	defer func() {
		time.Local = originalLocal
	}()

	dbPath := filepath.Join(t.TempDir(), "mission-control.db")
	bootstrapDB, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := bootstrapDB.Exec(`
		CREATE TABLE IF NOT EXISTS sessions_v2 (
			session_key TEXT PRIMARY KEY,
			agent_id TEXT NOT NULL DEFAULT 'default',
			parent_session_key TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT '',
			model_override TEXT NOT NULL DEFAULT '',
			think_level TEXT NOT NULL DEFAULT '',
			usage_mode TEXT NOT NULL DEFAULT 'off',
			verbose INTEGER NOT NULL DEFAULT 0,
			deliver INTEGER NOT NULL DEFAULT 0,
			queue_depth INTEGER NOT NULL DEFAULT 0,
			queue_mode TEXT NOT NULL DEFAULT 'collect',
			queue_debounce_ms INTEGER NOT NULL DEFAULT 1500,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			context_tokens INTEGER NOT NULL DEFAULT 0,
			message_count INTEGER NOT NULL DEFAULT 0,
			compaction_count INTEGER NOT NULL DEFAULT 0,
			last_summary TEXT NOT NULL DEFAULT '',
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`); err != nil {
		_ = bootstrapDB.Close()
		t.Fatalf("bootstrap sessions_v2 schema: %v", err)
	}
	if err := bootstrapDB.Close(); err != nil {
		t.Fatalf("bootstrapDB.Close() error = %v", err)
	}

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close()

	cfg := &config.Config{
		SoulPath: "/souls/default",
		AI: config.AIConfig{
			Model: "claude-sonnet-4-5",
		},
		Agents: []config.AgentConfig{
			{
				Name:         "researcher",
				SoulPath:     "/souls/researcher",
				Model:        "openai/gpt-4o",
				AllowedTools: []string{"search", "web_fetch"},
			},
		},
	}

	if _, err := store.SaveCronJobFull("0 0 9 * * *", "prepare daily research brief", 42, "llm", 0); err != nil {
		t.Fatalf("SaveCronJobFull(enabled) error = %v", err)
	}
	disabledJobID, err := store.SaveCronJobFull("0 30 11 * * *", "ping external uptime endpoint", 42, "exec", 120)
	if err != nil {
		t.Fatalf("SaveCronJobFull(disabled) error = %v", err)
	}
	if err := store.ToggleCronJob(disabledJobID, false); err != nil {
		t.Fatalf("ToggleCronJob(false) error = %v", err)
	}

	if err := store.RecordSubagentSpawn(
		"run-done",
		"agent:researcher:subagent:run-done",
		"agent:researcher:main",
		"researcher",
		"Collect launch updates",
		"openai/gpt-4o",
		"medium",
		"search,web_fetch",
		"/workspace/roles/researcher",
		true,
	); err != nil {
		t.Fatalf("RecordSubagentSpawn(run-done) error = %v", err)
	}
	if err := store.UpdateSubagentStatus("run-done", "done", "Delivered a release digest with three concrete action items.", ""); err != nil {
		t.Fatalf("UpdateSubagentStatus(run-done) error = %v", err)
	}

	if err := store.RecordSubagentSpawn(
		"run-error",
		"agent:researcher:subagent:run-error",
		"agent:researcher:main",
		"researcher",
		"Watch upstream deploys",
		"openai/gpt-4o",
		"low",
		"",
		"/workspace/roles/researcher",
		false,
	); err != nil {
		t.Fatalf("RecordSubagentSpawn(run-error) error = %v", err)
	}
	if err := store.UpdateSubagentStatus("run-error", "error", "", "upstream API returned 503"); err != nil {
		t.Fatalf("UpdateSubagentStatus(run-error) error = %v", err)
	}

	now := time.Date(2026, time.March, 15, 11, 0, 0, 0, time.Local)
	if _, err := store.DB().Exec(
		`UPDATE subagent_runs SET spawned_at = ?, completed_at = ? WHERE run_slug = ?`,
		now.Add(-2*time.Hour).UTC().Format("2006-01-02 15:04:05"),
		now.Add(-90*time.Minute).UTC().Format("2006-01-02 15:04:05"),
		"run-done",
	); err != nil {
		t.Fatalf("update run-done timestamps: %v", err)
	}
	if _, err := store.DB().Exec(
		`UPDATE subagent_runs SET spawned_at = ?, completed_at = ? WHERE run_slug = ?`,
		now.Add(-5*time.Hour).UTC().Format("2006-01-02 15:04:05"),
		now.Add(-4*time.Hour).UTC().Format("2006-01-02 15:04:05"),
		"run-error",
	); err != nil {
		t.Fatalf("update run-error timestamps: %v", err)
	}

	snapshot, err := loadMissionControlSnapshot(cfg, store, now)
	if err != nil {
		t.Fatalf("loadMissionControlSnapshot() error = %v", err)
	}

	if snapshot.Summary.RoleCount != 2 {
		t.Fatalf("RoleCount = %d, want 2", snapshot.Summary.RoleCount)
	}
	if snapshot.Summary.EnabledSchedules != 1 {
		t.Fatalf("EnabledSchedules = %d, want 1", snapshot.Summary.EnabledSchedules)
	}
	if snapshot.Summary.FailedRuns != 1 {
		t.Fatalf("FailedRuns = %d, want 1", snapshot.Summary.FailedRuns)
	}
	if snapshot.Summary.DeliveredToday != 1 {
		t.Fatalf("DeliveredToday = %d, want 1", snapshot.Summary.DeliveredToday)
	}

	if len(snapshot.Roles) != 2 {
		t.Fatalf("len(Roles) = %d, want 2", len(snapshot.Roles))
	}
	if snapshot.Roles[0].Name != "default" {
		t.Fatalf("default role = %q, want default", snapshot.Roles[0].Name)
	}
	if snapshot.Roles[1].Name != "researcher" {
		t.Fatalf("named role = %q, want researcher", snapshot.Roles[1].Name)
	}
	if snapshot.Roles[1].ToolPolicy != "search, web_fetch" {
		t.Fatalf("researcher tool policy = %q", snapshot.Roles[1].ToolPolicy)
	}

	if len(snapshot.Schedules) != 2 {
		t.Fatalf("len(Schedules) = %d, want 2", len(snapshot.Schedules))
	}
	if !snapshot.Schedules[0].Enabled {
		t.Fatalf("first schedule should be enabled")
	}
	if snapshot.Schedules[0].NextRun == "" {
		t.Fatalf("enabled schedule should have NextRun")
	}
	if snapshot.Schedules[1].Enabled {
		t.Fatalf("second schedule should be disabled")
	}
	if snapshot.Schedules[1].NextRun != "" {
		t.Fatalf("disabled schedule NextRun = %q, want empty", snapshot.Schedules[1].NextRun)
	}

	runBySlug := make(map[string]missionControlRun, len(snapshot.Runs))
	for _, run := range snapshot.Runs {
		runBySlug[run.RunSlug] = run
	}
	doneRun, ok := runBySlug["run-done"]
	if !ok {
		t.Fatalf("run-done missing from snapshot")
	}
	if doneRun.ValuePreview == "" {
		t.Fatalf("run-done should include value preview")
	}
	if doneRun.ToolPolicy != "search, web_fetch" {
		t.Fatalf("run-done tool policy = %q", doneRun.ToolPolicy)
	}

	errorRun, ok := runBySlug["run-error"]
	if !ok {
		t.Fatalf("run-error missing from snapshot")
	}
	if errorRun.ErrorPreview != "upstream API returned 503" {
		t.Fatalf("run-error error preview = %q", errorRun.ErrorPreview)
	}
	if errorRun.ToolPolicy != "All tools" {
		t.Fatalf("run-error tool policy = %q, want All tools", errorRun.ToolPolicy)
	}
}
