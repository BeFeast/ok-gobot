package storage

import "database/sql"

// ChatActivityRow holds aggregated activity stats for a single chat.
type ChatActivityRow struct {
	ChatID       int64
	AgentID      string
	MessageCount int
	LastActive   string
}

// GetChatActivityStats returns per-chat message counts and last-active times
// across both legacy and v2 session tables.
func (s *Store) GetChatActivityStats(limit int) ([]ChatActivityRow, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT
			COALESCE(sr.chat_id, 0) AS chat_id,
			sv.agent_id,
			sv.message_count,
			sv.updated_at
		FROM sessions_v2 sv
		LEFT JOIN session_routes sr ON sr.session_key = sv.session_key
		WHERE sv.message_count > 0
		ORDER BY sv.updated_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ChatActivityRow
	for rows.Next() {
		var r ChatActivityRow
		if err := rows.Scan(&r.ChatID, &r.AgentID, &r.MessageCount, &r.LastActive); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UserMessageRow holds a single user message with its timestamp.
type UserMessageRow struct {
	SessionKey string
	Content    string
	CreatedAt  string
}

// GetRecentUserMessages returns the most recent user-role messages across all
// sessions. This feeds the keyword/topic analysis in the role recommender.
func (s *Store) GetRecentUserMessages(limit int) ([]UserMessageRow, error) {
	if limit <= 0 {
		limit = 200
	}

	rows, err := s.db.Query(`
		SELECT session_key, content, created_at
		FROM session_messages_v2
		WHERE role = 'user'
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		// Fall back to legacy table if v2 table is empty or broken.
		return s.getRecentUserMessagesLegacy(limit)
	}
	defer rows.Close()

	var out []UserMessageRow
	for rows.Next() {
		var r UserMessageRow
		if err := rows.Scan(&r.SessionKey, &r.Content, &r.CreatedAt); err != nil {
			continue
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// If v2 is empty, try legacy.
	if len(out) == 0 {
		return s.getRecentUserMessagesLegacy(limit)
	}
	return out, nil
}

func (s *Store) getRecentUserMessagesLegacy(limit int) ([]UserMessageRow, error) {
	rows, err := s.db.Query(`
		SELECT CAST(chat_id AS TEXT), content, created_at
		FROM session_messages
		WHERE role = 'user'
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UserMessageRow
	for rows.Next() {
		var r UserMessageRow
		if err := rows.Scan(&r.SessionKey, &r.Content, &r.CreatedAt); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CronJobSummary returns all cron jobs (enabled or not). It wraps GetCronJobs
// so callers in the recommend package need only one import.
func (s *Store) CronJobSummary() ([]CronJob, error) {
	return s.GetCronJobs()
}

// JobKindCounts returns how many durable jobs exist per kind.
func (s *Store) JobKindCounts(limit int) (map[string]int, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT kind, COUNT(*) AS cnt
		FROM jobs
		GROUP BY kind
		ORDER BY cnt DESC
		LIMIT ?
	`, limit)
	if err != nil {
		if err == sql.ErrNoRows {
			return map[string]int{}, nil
		}
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var kind string
		var cnt int
		if err := rows.Scan(&kind, &cnt); err != nil {
			continue
		}
		out[kind] = cnt
	}
	return out, rows.Err()
}
