package memory

import (
	"context"
	"database/sql"
	"fmt"
)

// SQLiteSessionTranscriptSource wraps a *sql.DB that backs the storage.Store
// and exposes its session transcript tables to the memory indexer. It avoids
// importing the storage package so the dependency direction stays
// memory ← cli/app, not memory ↔ storage.
type SQLiteSessionTranscriptSource struct {
	db *sql.DB
}

// NewSQLiteSessionTranscriptSource constructs a transcript source backed by db.
func NewSQLiteSessionTranscriptSource(db *sql.DB) *SQLiteSessionTranscriptSource {
	return &SQLiteSessionTranscriptSource{db: db}
}

// ListSessionKeysForIndexing returns canonical session_keys present in
// sessions_v2 that have at least one indexable message. The list is not
// privacy-filtered; callers (typically the indexer) decide whether to skip
// group sessions.
func (s *SQLiteSessionTranscriptSource) ListSessionKeysForIndexing(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("session transcript source is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT session_key
		FROM session_messages_v2
		WHERE TRIM(content) <> ''
		  AND role IN ('user', 'assistant')
		ORDER BY session_key ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list session keys: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// LoadSessionMessages returns the transcript for sessionKey ordered by
// (created_at ASC, id ASC). System and tool roles are excluded; only user
// and assistant turns are returned.
func (s *SQLiteSessionTranscriptSource) LoadSessionMessages(ctx context.Context, sessionKey string) ([]SessionMessage, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("session transcript source is not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_key, role, content, COALESCE(run_id, ''), created_at
		FROM session_messages_v2
		WHERE session_key = ?
		  AND role IN ('user', 'assistant')
		ORDER BY created_at ASC, id ASC
	`, sessionKey)
	if err != nil {
		return nil, fmt.Errorf("load session messages: %w", err)
	}
	defer rows.Close()

	var msgs []SessionMessage
	for rows.Next() {
		var m SessionMessage
		if err := rows.Scan(&m.ID, &m.SessionKey, &m.Role, &m.Content, &m.RunID, &m.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}
