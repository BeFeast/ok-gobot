package storage

import (
	"strings"
	"time"
)

// TrainingSession summarises one session suitable for training-data export.
type TrainingSession struct {
	SessionKey   string
	AgentID      string
	MessageCount int
	TotalTokens  int
	CreatedAt    string
}

// TrainingFilter controls which sessions are included in the export.
type TrainingFilter struct {
	// Since / Until bound the creation time of sessions (zero = unbounded).
	Since time.Time
	Until time.Time
	// MinMessages discards sessions with fewer than this many messages.
	MinMessages int
	// WithJobs includes only sessions that were processed through the job
	// system (i.e. had at least one associated run_id).  This is the best
	// available proxy for "had tool calls" given the current schema.
	WithJobs bool
	// SuccessOnly further restricts WithJobs sessions to those that have at
	// least one job in the "succeeded" terminal state.
	SuccessOnly bool
}

// ListTrainingSessions returns sessions matching the filter ordered oldest
// first so that exported files are deterministic.
func (s *Store) ListTrainingSessions(f TrainingFilter) ([]TrainingSession, error) {
	var conds []string
	var args []interface{}

	minMsg := f.MinMessages
	if minMsg <= 0 {
		minMsg = 2 // always skip empty sessions
	}
	conds = append(conds, "sv.message_count >= ?")
	args = append(args, minMsg)

	if !f.Since.IsZero() {
		conds = append(conds, "sv.created_at >= ?")
		args = append(args, f.Since.UTC().Format("2006-01-02 15:04:05"))
	}
	if !f.Until.IsZero() {
		conds = append(conds, "sv.created_at <= ?")
		args = append(args, f.Until.UTC().Format("2006-01-02 15:04:05"))
	}

	if f.WithJobs || f.SuccessOnly {
		// Session must have at least one message tied to a job run.
		conds = append(conds, `EXISTS (
			SELECT 1 FROM session_messages_v2 m
			WHERE m.session_key = sv.session_key AND m.run_id != ''
		)`)
	}

	if f.SuccessOnly {
		// At least one succeeded job for this session.
		conds = append(conds, `EXISTS (
			SELECT 1 FROM jobs j
			WHERE j.session_key = sv.session_key AND j.status = 'succeeded'
		)`)
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	query := `
		SELECT sv.session_key, sv.agent_id, sv.message_count, sv.total_tokens, sv.created_at
		FROM sessions_v2 sv
		` + where + `
		ORDER BY sv.created_at ASC
	`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var sessions []TrainingSession
	for rows.Next() {
		var ts TrainingSession
		if err := rows.Scan(&ts.SessionKey, &ts.AgentID, &ts.MessageCount, &ts.TotalTokens, &ts.CreatedAt); err != nil {
			return nil, err
		}
		sessions = append(sessions, ts)
	}
	return sessions, rows.Err()
}

// GetTrainingMessages returns user and assistant messages for a session in
// chronological order (oldest first). Tool / system messages are excluded.
func (s *Store) GetTrainingMessages(sessionKey string) ([]SessionMessageV2, error) {
	rows, err := s.db.Query(`
		SELECT id, session_key, role, content, run_id, created_at
		FROM session_messages_v2
		WHERE session_key = ? AND role IN ('user', 'assistant')
		ORDER BY id ASC
	`, sessionKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var msgs []SessionMessageV2
	for rows.Next() {
		var m SessionMessageV2
		if err := rows.Scan(&m.ID, &m.SessionKey, &m.Role, &m.Content, &m.RunID, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}
