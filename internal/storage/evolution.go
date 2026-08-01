package storage

import (
	"database/sql"
	"encoding/json"
	"time"
)

// EvolutionMetric records per-task execution metrics for the evolution loop.
type EvolutionMetric struct {
	ID         int64
	TaskID     string
	SessionKey string
	Success    bool
	Tokens     int
	DurationMS int64
	Retries    int
	ToolCalls  []string // tool names used during the task
	CreatedAt  string
}

// EvolutionVersion records a versioned agent config snapshot.
type EvolutionVersion struct {
	ID             int64
	Version        int
	ConfigHash     string
	BenchmarkScore float64
	PromotedAt     string
	RolledBackAt   string // empty if not rolled back
	Notes          string
}

// evolutionMigrations returns DDL statements for the evolution tables.
// These are appended to the main migration list via AddEvolutionMigrations.
func evolutionMigrations() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS evolution_metrics (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id     TEXT NOT NULL,
			session_key TEXT NOT NULL DEFAULT '',
			success     INTEGER NOT NULL DEFAULT 0,
			tokens      INTEGER NOT NULL DEFAULT 0,
			duration_ms INTEGER NOT NULL DEFAULT 0,
			retries     INTEGER NOT NULL DEFAULT 0,
			tool_calls  TEXT NOT NULL DEFAULT '[]',
			created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_evolution_metrics_task_id ON evolution_metrics(task_id)`,
		`CREATE INDEX IF NOT EXISTS idx_evolution_metrics_created ON evolution_metrics(created_at)`,
		`CREATE TABLE IF NOT EXISTS evolution_versions (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			version         INTEGER NOT NULL UNIQUE,
			config_hash     TEXT NOT NULL DEFAULT '',
			benchmark_score REAL NOT NULL DEFAULT 0,
			promoted_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			rolled_back_at  DATETIME NOT NULL DEFAULT '',
			notes           TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_evolution_versions_version ON evolution_versions(version)`,
	}
}

// RecordTaskMetric persists a single task execution metric.
func (s *Store) RecordTaskMetric(m EvolutionMetric) error {
	toolCallsJSON, err := json.Marshal(m.ToolCalls)
	if err != nil {
		toolCallsJSON = []byte("[]")
	}

	success := 0
	if m.Success {
		success = 1
	}

	_, err = s.db.Exec(`
		INSERT INTO evolution_metrics
			(task_id, session_key, success, tokens, duration_ms, retries, tool_calls, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		m.TaskID,
		m.SessionKey,
		success,
		m.Tokens,
		m.DurationMS,
		m.Retries,
		string(toolCallsJSON),
		time.Now().UTC().Format("2006-01-02 15:04:05"),
	)
	return err
}

// CountTaskMetricsSince returns the number of metrics recorded since a given time.
func (s *Store) CountTaskMetricsSince(since time.Time) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM evolution_metrics WHERE created_at >= ?`,
		since.UTC().Format("2006-01-02 15:04:05"),
	).Scan(&count)
	return count, err
}

// GetTaskMetricsSince returns all metrics recorded since a given time.
func (s *Store) GetTaskMetricsSince(since time.Time, limit int) ([]EvolutionMetric, error) {
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.db.Query(`
		SELECT id, task_id, session_key, success, tokens, duration_ms, retries, tool_calls, created_at
		FROM evolution_metrics
		WHERE created_at >= ?
		ORDER BY created_at DESC
		LIMIT ?
	`, since.UTC().Format("2006-01-02 15:04:05"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMetrics(rows)
}

// GetRecentTaskMetrics returns the most recent N task metrics.
func (s *Store) GetRecentTaskMetrics(limit int) ([]EvolutionMetric, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT id, task_id, session_key, success, tokens, duration_ms, retries, tool_calls, created_at
		FROM evolution_metrics
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMetrics(rows)
}

func scanMetrics(rows *sql.Rows) ([]EvolutionMetric, error) {
	var out []EvolutionMetric
	for rows.Next() {
		var m EvolutionMetric
		var success int
		var toolCallsJSON string
		if err := rows.Scan(
			&m.ID, &m.TaskID, &m.SessionKey, &success,
			&m.Tokens, &m.DurationMS, &m.Retries, &toolCallsJSON, &m.CreatedAt,
		); err != nil {
			return nil, err
		}
		m.Success = success != 0
		if err := json.Unmarshal([]byte(toolCallsJSON), &m.ToolCalls); err != nil {
			m.ToolCalls = nil
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RecordEvolutionVersion persists a new agent version after a successful gate pass.
func (s *Store) RecordEvolutionVersion(v EvolutionVersion) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO evolution_versions (version, config_hash, benchmark_score, promoted_at, notes)
		VALUES (?, ?, ?, ?, ?)
	`,
		v.Version,
		v.ConfigHash,
		v.BenchmarkScore,
		time.Now().UTC().Format("2006-01-02 15:04:05"),
		v.Notes,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MarkVersionRolledBack sets the rolled_back_at timestamp for a version.
func (s *Store) MarkVersionRolledBack(version int) error {
	_, err := s.db.Exec(`
		UPDATE evolution_versions
		SET rolled_back_at = ?
		WHERE version = ?
	`, time.Now().UTC().Format("2006-01-02 15:04:05"), version)
	return err
}

// GetLatestEvolutionVersion returns the most recently promoted (and not rolled back) version.
func (s *Store) GetLatestEvolutionVersion() (*EvolutionVersion, error) {
	row := s.db.QueryRow(`
		SELECT id, version, config_hash, benchmark_score, promoted_at,
		       COALESCE(rolled_back_at, '') AS rolled_back_at, notes
		FROM evolution_versions
		WHERE rolled_back_at = '' OR rolled_back_at IS NULL
		ORDER BY version DESC
		LIMIT 1
	`)
	return scanVersion(row)
}

// GetEvolutionVersion returns a specific version by number.
func (s *Store) GetEvolutionVersion(version int) (*EvolutionVersion, error) {
	row := s.db.QueryRow(`
		SELECT id, version, config_hash, benchmark_score, promoted_at,
		       COALESCE(rolled_back_at, '') AS rolled_back_at, notes
		FROM evolution_versions
		WHERE version = ?
	`, version)
	return scanVersion(row)
}

// ListEvolutionVersions returns all versions in descending order.
func (s *Store) ListEvolutionVersions(limit int) ([]EvolutionVersion, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, version, config_hash, benchmark_score, promoted_at,
		       COALESCE(rolled_back_at, '') AS rolled_back_at, notes
		FROM evolution_versions
		ORDER BY version DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EvolutionVersion
	for rows.Next() {
		var v EvolutionVersion
		if err := rows.Scan(&v.ID, &v.Version, &v.ConfigHash, &v.BenchmarkScore,
			&v.PromotedAt, &v.RolledBackAt, &v.Notes); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetEvolutionMetricsSummary returns aggregate stats for metrics analysis.
type EvolutionMetricsSummary struct {
	Total         int
	Successes     int
	Failures      int
	SuccessRate   float64
	AvgTokens     float64
	AvgDurationMS float64
	TopToolCalls  map[string]int
}

// SummarizeRecentMetrics computes aggregate statistics over the last N metrics.
func (s *Store) SummarizeRecentMetrics(limit int) (*EvolutionMetricsSummary, error) {
	metrics, err := s.GetRecentTaskMetrics(limit)
	if err != nil {
		return nil, err
	}

	summary := &EvolutionMetricsSummary{
		TopToolCalls: make(map[string]int),
	}
	var totalTokens, totalDuration int64
	for _, m := range metrics {
		summary.Total++
		if m.Success {
			summary.Successes++
		} else {
			summary.Failures++
		}
		totalTokens += int64(m.Tokens)
		totalDuration += m.DurationMS
		for _, tool := range m.ToolCalls {
			summary.TopToolCalls[tool]++
		}
	}
	if summary.Total > 0 {
		summary.SuccessRate = float64(summary.Successes) / float64(summary.Total)
		summary.AvgTokens = float64(totalTokens) / float64(summary.Total)
		summary.AvgDurationMS = float64(totalDuration) / float64(summary.Total)
	}
	return summary, nil
}

// GetLastEvolutionTime returns the time of the most recent promoted version.
// Returns zero time if no versions exist.
func (s *Store) GetLastEvolutionTime() (time.Time, error) {
	var ts string
	err := s.db.QueryRow(
		`SELECT COALESCE(MAX(promoted_at), '') FROM evolution_versions`,
	).Scan(&ts)
	if err != nil || ts == "" {
		return time.Time{}, err
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05Z", "2006-01-02T15:04:05-07:00"} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, nil
		}
	}
	return time.Time{}, nil
}

func scanVersion(row *sql.Row) (*EvolutionVersion, error) {
	var v EvolutionVersion
	err := row.Scan(&v.ID, &v.Version, &v.ConfigHash, &v.BenchmarkScore,
		&v.PromotedAt, &v.RolledBackAt, &v.Notes)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}
