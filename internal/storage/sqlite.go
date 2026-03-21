package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// Store provides data persistence
type Store struct {
	db *sql.DB
}

const (
	defaultAgentID        = "default"
	defaultUsageMode      = "off"
	defaultQueueMode      = "interrupt"
	defaultQueueDebounce  = 1500
	defaultRouteTransport = "telegram"
	emergencyStopStateKey = "estop_enabled"
)

// New creates a new storage instance
func New(dbPath string) (*Store, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create storage directory: %w", err)
	}

	// Open database
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Test connection
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	store := &Store{db: db}

	// Run migrations
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("failed to migrate database: %w", err)
	}

	return store, nil
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection
func (s *Store) DB() *sql.DB {
	return s.db
}

// migrate runs database migrations
func (s *Store) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER NOT NULL,
			message_id INTEGER NOT NULL,
			user_id INTEGER,
			username TEXT,
			content TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_messages_chat_id ON messages(chat_id);`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER UNIQUE NOT NULL,
			state TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_chat_id ON sessions(chat_id);`,
		// Session messages table for full conversation history
		`CREATE TABLE IF NOT EXISTS session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (session_id) REFERENCES sessions(id)
		);`,
		`CREATE INDEX IF NOT EXISTS idx_session_messages_session ON session_messages(session_id);`,
		`CREATE INDEX IF NOT EXISTS idx_session_messages_chat ON session_messages(chat_id);`,
		// Session metadata
		`ALTER TABLE sessions ADD COLUMN message_count INTEGER DEFAULT 0;`,
		`ALTER TABLE sessions ADD COLUMN last_summary TEXT;`,
		`ALTER TABLE sessions ADD COLUMN compaction_count INTEGER DEFAULT 0;`,
		`ALTER TABLE sessions ADD COLUMN model_override TEXT DEFAULT '';`,
		`ALTER TABLE sessions ADD COLUMN group_mode TEXT DEFAULT '';`,
		`ALTER TABLE sessions ADD COLUMN active_agent TEXT DEFAULT 'default';`,
		// Token usage tracking
		`ALTER TABLE sessions ADD COLUMN input_tokens INTEGER DEFAULT 0;`,
		`ALTER TABLE sessions ADD COLUMN output_tokens INTEGER DEFAULT 0;`,
		`ALTER TABLE sessions ADD COLUMN total_tokens INTEGER DEFAULT 0;`,
		`ALTER TABLE sessions ADD COLUMN context_tokens INTEGER DEFAULT 0;`,
		`ALTER TABLE sessions ADD COLUMN usage_mode TEXT DEFAULT 'off';`,
		`ALTER TABLE sessions ADD COLUMN think_level TEXT DEFAULT '';`,
		`ALTER TABLE sessions ADD COLUMN verbose INTEGER DEFAULT 0;`,
		`ALTER TABLE sessions ADD COLUMN queue_mode TEXT DEFAULT 'interrupt';`,
		`ALTER TABLE sessions ADD COLUMN queue_debounce_ms INTEGER DEFAULT 1500;`,
		// Cron jobs table
		`CREATE TABLE IF NOT EXISTS cron_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			expression TEXT NOT NULL,
			task TEXT NOT NULL,
			chat_id INTEGER NOT NULL,
			next_run DATETIME,
			enabled INTEGER DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_cron_jobs_next_run ON cron_jobs(next_run);`,
		// Authorized users table
		`CREATE TABLE IF NOT EXISTS authorized_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER UNIQUE NOT NULL,
			username TEXT,
			authorized_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			paired_by TEXT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_authorized_users_user_id ON authorized_users(user_id);`,
		// Global app state table for operator/runtime flags.
		`CREATE TABLE IF NOT EXISTS app_state (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT '',
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		// Sub-agent runs table
		`CREATE TABLE IF NOT EXISTS subagent_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL DEFAULT '',
			run_slug TEXT UNIQUE NOT NULL,
			session_key TEXT NOT NULL,
			child_session_key TEXT NOT NULL DEFAULT '',
			parent_session_key TEXT NOT NULL,
			agent_id TEXT NOT NULL,
			task TEXT NOT NULL,
			model TEXT DEFAULT '',
			thinking TEXT DEFAULT '',
			tool_allowlist TEXT DEFAULT '',
			workspace_root TEXT DEFAULT '',
			deliver_back INTEGER DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'pending',
			result TEXT DEFAULT '',
			error TEXT DEFAULT '',
			spawned_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME
		);`,
		`CREATE INDEX IF NOT EXISTS idx_subagent_runs_parent ON subagent_runs(parent_session_key);`,
		`CREATE INDEX IF NOT EXISTS idx_subagent_runs_status ON subagent_runs(status);`,
		// ── v2 session tables ────────────────────────────────────────────────
		// sessions_v2: canonical session keyed by session_key string instead of chat_id integer.
		`CREATE TABLE IF NOT EXISTS sessions_v2 (
			session_key          TEXT PRIMARY KEY,
			agent_id             TEXT NOT NULL DEFAULT '',
			parent_session_key   TEXT DEFAULT '',
			state                TEXT DEFAULT '',
			model_override       TEXT DEFAULT '',
			think_level          TEXT DEFAULT '',
			active_agent         TEXT DEFAULT 'default',
			usage_mode           TEXT DEFAULT 'off',
			verbose              INTEGER DEFAULT 0,
			deliver              INTEGER DEFAULT 0,
			queue_depth          INTEGER DEFAULT 0,
			queue_mode           TEXT DEFAULT 'interrupt',
			queue_debounce_ms    INTEGER DEFAULT 1500,
			message_count        INTEGER DEFAULT 0,
			input_tokens         INTEGER DEFAULT 0,
			output_tokens        INTEGER DEFAULT 0,
			total_tokens         INTEGER DEFAULT 0,
			context_tokens       INTEGER DEFAULT 0,
			compaction_count     INTEGER DEFAULT 0,
			last_summary         TEXT DEFAULT '',
			promoted_from_chat_id INTEGER DEFAULT 0,
			created_at           DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at           DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`ALTER TABLE sessions_v2 ADD COLUMN state TEXT DEFAULT '';`,
		`ALTER TABLE sessions_v2 ADD COLUMN deliver INTEGER DEFAULT 0;`,
		`ALTER TABLE sessions_v2 ADD COLUMN queue_depth INTEGER DEFAULT 0;`,
		// session_messages_v2: full transcript history, keyed by session_key.
		`CREATE TABLE IF NOT EXISTS session_messages_v2 (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			session_key TEXT NOT NULL,
			role        TEXT NOT NULL,
			content     TEXT NOT NULL,
			run_id      TEXT DEFAULT '',
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_session_messages_v2_key ON session_messages_v2(session_key);`,
		// session_routes: maps canonical session_key → delivery channel details.
		`CREATE TABLE IF NOT EXISTS session_routes (
				session_key TEXT PRIMARY KEY,
				channel     TEXT NOT NULL,
				chat_id     INTEGER NOT NULL,
				thread_id   INTEGER NOT NULL DEFAULT 0,
				reply_to_message_id INTEGER NOT NULL DEFAULT 0,
				user_id     INTEGER NOT NULL DEFAULT 0,
				username    TEXT NOT NULL DEFAULT '',
				updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			);`,
		`ALTER TABLE session_routes ADD COLUMN channel TEXT NOT NULL DEFAULT 'telegram';`,
		`ALTER TABLE session_routes ADD COLUMN chat_id INTEGER NOT NULL DEFAULT 0;`,
		`ALTER TABLE session_routes ADD COLUMN thread_id INTEGER NOT NULL DEFAULT 0;`,
		`ALTER TABLE session_routes ADD COLUMN reply_to_message_id INTEGER NOT NULL DEFAULT 0;`,
		`ALTER TABLE session_routes ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0;`,
		`ALTER TABLE session_routes ADD COLUMN username TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE session_routes ADD COLUMN updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP;`,
		// Cron job type: "llm" (default, goes through AI agent) or "exec" (direct shell execution)
		`ALTER TABLE cron_jobs ADD COLUMN type TEXT NOT NULL DEFAULT 'llm';`,
		// Cron job timeout in seconds (0 = use default)
		`ALTER TABLE cron_jobs ADD COLUMN timeout_seconds INTEGER NOT NULL DEFAULT 0;`,
	}

	for _, migration := range migrations {
		if _, err := s.db.Exec(migration); err != nil {
			// Ignore "duplicate column" errors from ALTER TABLE on re-runs
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	if err := s.migrateCanonicalSchema(); err != nil {
		return err
	}

	return nil
}

// migrateCanonicalSchema creates and backfills the Phase-B canonical schema.
// Legacy tables remain in place for backward compatibility.
func (s *Store) migrateCanonicalSchema() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS sessions_v2 (
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
			queue_mode TEXT NOT NULL DEFAULT 'interrupt',
			queue_debounce_ms INTEGER NOT NULL DEFAULT 1500,
			input_tokens INTEGER NOT NULL DEFAULT 0,
			output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			context_tokens INTEGER NOT NULL DEFAULT 0,
			message_count INTEGER NOT NULL DEFAULT 0,
			compaction_count INTEGER NOT NULL DEFAULT 0,
			last_summary TEXT NOT NULL DEFAULT '',
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`ALTER TABLE sessions_v2 ADD COLUMN state TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE sessions_v2 ADD COLUMN deliver INTEGER NOT NULL DEFAULT 0;`,
		`ALTER TABLE sessions_v2 ADD COLUMN queue_depth INTEGER NOT NULL DEFAULT 0;`,
		`ALTER TABLE sessions_v2 ADD COLUMN active_agent TEXT NOT NULL DEFAULT 'default';`,
		`ALTER TABLE sessions_v2 ADD COLUMN promoted_from_chat_id INTEGER NOT NULL DEFAULT 0;`,
		`ALTER TABLE sessions_v2 ADD COLUMN created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP;`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_v2_agent_id ON sessions_v2(agent_id);`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_v2_parent ON sessions_v2(parent_session_key);`,
		`CREATE TABLE IF NOT EXISTS session_routes (
			session_key TEXT PRIMARY KEY,
			channel TEXT NOT NULL,
			chat_id INTEGER NOT NULL,
			thread_id INTEGER NOT NULL DEFAULT 0,
			reply_to_message_id INTEGER NOT NULL DEFAULT 0,
			user_id INTEGER NOT NULL DEFAULT 0,
			username TEXT NOT NULL DEFAULT '',
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_session_routes_chat_id ON session_routes(chat_id);`,
		`CREATE TABLE IF NOT EXISTS session_messages_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_key TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			run_id TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_session_messages_v2_session ON session_messages_v2(session_key);`,
		`CREATE INDEX IF NOT EXISTS idx_session_messages_v2_created ON session_messages_v2(created_at);`,
		`CREATE TABLE IF NOT EXISTS run_queue_state (
			session_key TEXT PRIMARY KEY,
			depth INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS jobs (
			job_id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			worker TEXT NOT NULL DEFAULT '',
			session_key TEXT NOT NULL DEFAULT '',
			delivery_session_key TEXT NOT NULL DEFAULT '',
			retry_of_job_id TEXT NOT NULL DEFAULT '',
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending',
			cancel_requested INTEGER NOT NULL DEFAULT 0,
			attempt INTEGER NOT NULL DEFAULT 1,
			max_attempts INTEGER NOT NULL DEFAULT 1,
			timeout_seconds INTEGER NOT NULL DEFAULT 0,
			summary TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			started_at DATETIME,
			completed_at DATETIME,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_delivery_session_key ON jobs(delivery_session_key);`,
		`CREATE INDEX IF NOT EXISTS idx_jobs_retry_of_job_id ON jobs(retry_of_job_id);`,
		`CREATE TABLE IF NOT EXISTS job_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id TEXT NOT NULL,
			event_type TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			payload TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_job_events_job_id ON job_events(job_id, created_at);`,
		`CREATE TABLE IF NOT EXISTS job_artifacts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			job_id TEXT NOT NULL,
			name TEXT NOT NULL,
			artifact_type TEXT NOT NULL,
			mime_type TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			uri TEXT NOT NULL DEFAULT '',
			metadata TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_job_artifacts_job_id ON job_artifacts(job_id, created_at);`,
		`ALTER TABLE subagent_runs ADD COLUMN run_id TEXT NOT NULL DEFAULT '';`,
		`ALTER TABLE subagent_runs ADD COLUMN child_session_key TEXT NOT NULL DEFAULT '';`,
		`CREATE INDEX IF NOT EXISTS idx_subagent_runs_run_id ON subagent_runs(run_id);`,
		`CREATE INDEX IF NOT EXISTS idx_subagent_runs_child ON subagent_runs(child_session_key);`,
	}

	for _, migration := range migrations {
		if _, err := s.db.Exec(migration); err != nil {
			if strings.Contains(err.Error(), "duplicate column") {
				continue
			}
			return fmt.Errorf("canonical schema migration failed: %w", err)
		}
	}

	if _, err := s.db.Exec(`
		UPDATE subagent_runs
		SET run_id = run_slug
		WHERE run_id = ''
	`); err != nil {
		return fmt.Errorf("canonical schema migration failed: %w", err)
	}
	if _, err := s.db.Exec(`
		UPDATE subagent_runs
		SET child_session_key = session_key
		WHERE child_session_key = ''
	`); err != nil {
		return fmt.Errorf("canonical schema migration failed: %w", err)
	}

	// Migrate queue_mode default: collect → interrupt.
	// Sessions created before the default change have 'collect' stored explicitly.
	s.db.Exec(`UPDATE sessions SET queue_mode = 'interrupt' WHERE queue_mode = 'collect'`)
	s.db.Exec(`UPDATE sessions_v2 SET queue_mode = 'interrupt' WHERE queue_mode = 'collect'`)

	if err := s.backfillCanonicalSessionData(); err != nil {
		return err
	}

	return nil
}

func (s *Store) backfillCanonicalSessionData() error {
	if _, err := s.db.Exec(`
		INSERT OR REPLACE INTO sessions_v2 (
			session_key,
			agent_id,
			parent_session_key,
			state,
			model_override,
			think_level,
			usage_mode,
			verbose,
			deliver,
			queue_depth,
			queue_mode,
			queue_debounce_ms,
			input_tokens,
			output_tokens,
			total_tokens,
			context_tokens,
			message_count,
			compaction_count,
			last_summary,
			updated_at
		)
		SELECT
			'agent:' || COALESCE(NULLIF(TRIM(active_agent), ''), 'default') || ':telegram:group:' || CAST(chat_id AS TEXT),
			COALESCE(NULLIF(TRIM(active_agent), ''), 'default'),
			'',
			COALESCE(state, ''),
			COALESCE(model_override, ''),
			COALESCE(think_level, ''),
			COALESCE(NULLIF(usage_mode, ''), 'off'),
			COALESCE(verbose, 0),
			0,
			0,
			COALESCE(NULLIF(queue_mode, ''), 'interrupt'),
			COALESCE(NULLIF(queue_debounce_ms, 0), 1500),
			COALESCE(input_tokens, 0),
			COALESCE(output_tokens, 0),
			COALESCE(total_tokens, 0),
			COALESCE(context_tokens, 0),
			COALESCE(message_count, 0),
			COALESCE(compaction_count, 0),
			COALESCE(last_summary, ''),
			COALESCE(updated_at, CURRENT_TIMESTAMP)
		FROM sessions
	`); err != nil {
		return fmt.Errorf("canonical session backfill failed: %w", err)
	}

	if _, err := s.db.Exec(`
		INSERT INTO session_messages_v2 (session_key, role, content, created_at)
		SELECT
			'agent:' || COALESCE(NULLIF(TRIM(COALESCE(s.active_agent, '')), ''), 'default') || ':telegram:group:' || CAST(sm.chat_id AS TEXT),
			sm.role,
			sm.content,
			COALESCE(sm.created_at, CURRENT_TIMESTAMP)
		FROM session_messages sm
		LEFT JOIN sessions s ON s.chat_id = sm.chat_id
		WHERE NOT EXISTS (
			SELECT 1
			FROM session_messages_v2 v2
			WHERE v2.session_key = ('agent:' || COALESCE(NULLIF(TRIM(COALESCE(s.active_agent, '')), ''), 'default') || ':telegram:group:' || CAST(sm.chat_id AS TEXT))
			  AND v2.role = sm.role
			  AND v2.content = sm.content
			  AND v2.created_at = COALESCE(sm.created_at, CURRENT_TIMESTAMP)
		)
	`); err != nil {
		return fmt.Errorf("canonical message backfill failed: %w", err)
	}

	if _, err := s.db.Exec(`
		INSERT OR REPLACE INTO run_queue_state (session_key, depth, updated_at)
		SELECT session_key, queue_depth, updated_at
		FROM sessions_v2
		WHERE queue_depth > 0
	`); err != nil {
		return fmt.Errorf("canonical queue-state backfill failed: %w", err)
	}

	return nil
}

func normalizeAgentID(agentID string) string {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return defaultAgentID
	}
	return agentID
}

func normalizeUsageMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return defaultUsageMode
	}
	return mode
}

func normalizeQueueMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return defaultQueueMode
	}
	return mode
}

func canonicalTelegramSessionKey(chatID int64, agentID string) string {
	return fmt.Sprintf("agent:%s:telegram:group:%d", normalizeAgentID(agentID), chatID)
}

func (s *Store) ensureSessionV2(sessionKey, agentID, parentSessionKey string) error {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return nil
	}
	agentID = normalizeAgentID(agentID)
	_, err := s.db.Exec(`
		INSERT INTO sessions_v2 (
			session_key, agent_id, parent_session_key, usage_mode, queue_mode, queue_debounce_ms
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
			agent_id = excluded.agent_id,
			parent_session_key = excluded.parent_session_key,
			updated_at = CURRENT_TIMESTAMP
	`, sessionKey, agentID, parentSessionKey, defaultUsageMode, defaultQueueMode, defaultQueueDebounce)
	return err
}

func (s *Store) syncSessionV2ByChatID(chatID int64) (string, error) {
	var (
		state         sql.NullString
		modelOverride sql.NullString
		thinkLevel    sql.NullString
		usageMode     sql.NullString
		activeAgent   sql.NullString
		queueMode     sql.NullString
		lastSummary   sql.NullString
		verboseInt    int
		queueDebounce int
		inputTokens   int
		outputTokens  int
		totalTokens   int
		contextTokens int
		messageCount  int
		compactionCnt int
	)

	err := s.db.QueryRow(`
		SELECT
			state,
			model_override,
			think_level,
			usage_mode,
			active_agent,
			queue_mode,
			last_summary,
			COALESCE(verbose, 0),
			COALESCE(queue_debounce_ms, 0),
			COALESCE(input_tokens, 0),
			COALESCE(output_tokens, 0),
			COALESCE(total_tokens, 0),
			COALESCE(context_tokens, 0),
			COALESCE(message_count, 0),
			COALESCE(compaction_count, 0)
		FROM sessions
		WHERE chat_id = ?
	`, chatID).Scan(
		&state,
		&modelOverride,
		&thinkLevel,
		&usageMode,
		&activeAgent,
		&queueMode,
		&lastSummary,
		&verboseInt,
		&queueDebounce,
		&inputTokens,
		&outputTokens,
		&totalTokens,
		&contextTokens,
		&messageCount,
		&compactionCnt,
	)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	agentID := normalizeAgentID(activeAgent.String)
	sessionKey := canonicalTelegramSessionKey(chatID, agentID)
	pattern := fmt.Sprintf("agent:*:telegram:group:%d", chatID)
	if _, err := s.db.Exec(`
		DELETE FROM sessions_v2
		WHERE session_key GLOB ? AND session_key <> ?
	`, pattern, sessionKey); err != nil {
		return "", err
	}

	queueDepth := 0
	switch err := s.db.QueryRow("SELECT depth FROM run_queue_state WHERE session_key = ?", sessionKey).Scan(&queueDepth); err {
	case nil:
	case sql.ErrNoRows:
	default:
		return "", err
	}

	queueDebounceValue := queueDebounce
	if queueDebounceValue <= 0 {
		queueDebounceValue = defaultQueueDebounce
	}

	_, err = s.db.Exec(`
		INSERT INTO sessions_v2 (
			session_key,
			agent_id,
			parent_session_key,
			state,
			model_override,
			think_level,
			usage_mode,
			verbose,
			deliver,
			queue_depth,
			queue_mode,
			queue_debounce_ms,
			input_tokens,
			output_tokens,
			total_tokens,
			context_tokens,
			message_count,
			compaction_count,
			last_summary
		) VALUES (?, ?, '', ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
			agent_id = excluded.agent_id,
			state = excluded.state,
			model_override = excluded.model_override,
			think_level = excluded.think_level,
			usage_mode = excluded.usage_mode,
			verbose = excluded.verbose,
			queue_depth = excluded.queue_depth,
			queue_mode = excluded.queue_mode,
			queue_debounce_ms = excluded.queue_debounce_ms,
			input_tokens = excluded.input_tokens,
			output_tokens = excluded.output_tokens,
			total_tokens = excluded.total_tokens,
			context_tokens = excluded.context_tokens,
			message_count = excluded.message_count,
			compaction_count = excluded.compaction_count,
			last_summary = excluded.last_summary,
			updated_at = CURRENT_TIMESTAMP
	`, sessionKey, agentID, state.String, modelOverride.String, thinkLevel.String, normalizeUsageMode(usageMode.String), verboseInt,
		queueDepth, normalizeQueueMode(queueMode.String), queueDebounceValue, inputTokens, outputTokens, totalTokens, contextTokens,
		messageCount, compactionCnt, lastSummary.String)
	if err != nil {
		return "", err
	}
	return sessionKey, nil
}

// SaveMessage stores a message
func (s *Store) SaveMessage(chatID, messageID, userID int64, username, content string) error {
	_, err := s.db.Exec(
		"INSERT INTO messages (chat_id, message_id, user_id, username, content) VALUES (?, ?, ?, ?, ?)",
		chatID, messageID, userID, username, content,
	)
	return err
}

// GetSession retrieves session state for a chat
func (s *Store) GetSession(chatID int64) (string, error) {
	var state string
	err := s.db.QueryRow("SELECT state FROM sessions WHERE chat_id = ?", chatID).Scan(&state)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return state, err
}

// SaveSession stores session state for a chat
func (s *Store) SaveSession(chatID int64, state string) error {
	_, err := s.db.Exec(
		"INSERT INTO sessions (chat_id, state, queue_mode) VALUES (?, ?, ?) ON CONFLICT(chat_id) DO UPDATE SET state = excluded.state, updated_at = CURRENT_TIMESTAMP",
		chatID, state, defaultQueueMode,
	)
	if err != nil {
		return err
	}
	_, err = s.syncSessionV2ByChatID(chatID)
	return err
}

// SessionMessage represents a message in a session
type SessionMessage struct {
	ID        int64
	SessionID int64
	ChatID    int64
	Role      string
	Content   string
	CreatedAt string
}

// SaveSessionMessage stores a message in the session history
func (s *Store) SaveSessionMessage(chatID int64, role, content string) error {
	// Get or create session
	var sessionID int64
	err := s.db.QueryRow("SELECT id FROM sessions WHERE chat_id = ?", chatID).Scan(&sessionID)
	if err == sql.ErrNoRows {
		result, err := s.db.Exec("INSERT INTO sessions (chat_id, state, queue_mode) VALUES (?, '', ?)", chatID, defaultQueueMode)
		if err != nil {
			return err
		}
		sessionID, _ = result.LastInsertId()
	} else if err != nil {
		return err
	}

	_, err = s.db.Exec(
		"INSERT INTO session_messages (session_id, chat_id, role, content) VALUES (?, ?, ?, ?)",
		sessionID, chatID, role, content,
	)
	if err != nil {
		return err
	}

	// Update message count
	_, err = s.db.Exec(
		"UPDATE sessions SET message_count = message_count + 1 WHERE id = ?",
		sessionID,
	)
	if err != nil {
		return err
	}

	sessionKey, err := s.syncSessionV2ByChatID(chatID)
	if err != nil {
		return err
	}
	if sessionKey != "" {
		if _, err := s.db.Exec(
			"INSERT INTO session_messages_v2 (session_key, role, content) VALUES (?, ?, ?)",
			sessionKey, role, content,
		); err != nil {
			return err
		}
	}

	return nil
}

// GetSessionMessages retrieves messages for a chat session
func (s *Store) GetSessionMessages(chatID int64, limit int) ([]SessionMessage, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT id, session_id, chat_id, role, content, created_at 
		FROM session_messages 
		WHERE chat_id = ? 
		ORDER BY created_at DESC 
		LIMIT ?
	`, chatID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []SessionMessage
	for rows.Next() {
		var msg SessionMessage
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.ChatID, &msg.Role, &msg.Content, &msg.CreatedAt); err != nil {
			return nil, err
		}
		messages = append(messages, msg)
	}

	// Reverse to get chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	return messages, nil
}

// ListSessions returns all active sessions with metadata
func (s *Store) ListSessions(limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := s.db.Query(`
		SELECT id, chat_id, message_count, updated_at 
		FROM sessions 
		ORDER BY updated_at DESC 
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []map[string]interface{}
	for rows.Next() {
		var id, chatID int64
		var messageCount int
		var updatedAt string
		if err := rows.Scan(&id, &chatID, &messageCount, &updatedAt); err != nil {
			continue
		}
		sessions = append(sessions, map[string]interface{}{
			"id":            id,
			"chat_id":       chatID,
			"message_count": messageCount,
			"updated_at":    updatedAt,
		})
	}

	return sessions, nil
}

// SaveSessionSummary stores a compaction summary
func (s *Store) SaveSessionSummary(chatID int64, summary string) error {
	_, err := s.db.Exec(`
		UPDATE sessions 
		SET last_summary = ?, compaction_count = compaction_count + 1 
		WHERE chat_id = ?
	`, summary, chatID)
	if err != nil {
		return err
	}
	_, err = s.syncSessionV2ByChatID(chatID)
	return err
}

// CronJob represents a scheduled task
type CronJob struct {
	ID             int64
	Expression     string
	Task           string
	ChatID         int64
	NextRun        string
	Enabled        bool
	CreatedAt      string
	Type           string // "llm" (AI agent) or "exec" (direct shell)
	TimeoutSeconds int    // 0 = use default
}

// SaveCronJob creates or updates a cron job
func (s *Store) SaveCronJob(expression, task string, chatID int64) (int64, error) {
	return s.SaveCronJobFull(expression, task, chatID, "llm", 0)
}

// SaveCronJobFull creates a cron job with all parameters
func (s *Store) SaveCronJobFull(expression, task string, chatID int64, jobType string, timeoutSeconds int) (int64, error) {
	if jobType == "" {
		jobType = "llm"
	}
	result, err := s.db.Exec(
		"INSERT INTO cron_jobs (expression, task, chat_id, type, timeout_seconds) VALUES (?, ?, ?, ?, ?)",
		expression, task, chatID, jobType, timeoutSeconds,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetCronJobs returns all enabled cron jobs
func (s *Store) GetCronJobs() ([]CronJob, error) {
	rows, err := s.db.Query(`
		SELECT id, expression, task, chat_id, next_run, enabled, created_at,
		       COALESCE(type, 'llm'), COALESCE(timeout_seconds, 0)
		FROM cron_jobs 
		WHERE enabled = 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []CronJob
	for rows.Next() {
		var job CronJob
		var nextRun sql.NullString
		if err := rows.Scan(&job.ID, &job.Expression, &job.Task, &job.ChatID, &nextRun, &job.Enabled, &job.CreatedAt, &job.Type, &job.TimeoutSeconds); err != nil {
			continue
		}
		if nextRun.Valid {
			job.NextRun = nextRun.String
		}
		jobs = append(jobs, job)
	}

	return jobs, nil
}

// DeleteCronJob removes a cron job
func (s *Store) DeleteCronJob(id int64) error {
	_, err := s.db.Exec("DELETE FROM cron_jobs WHERE id = ?", id)
	return err
}

// ToggleCronJob enables or disables a cron job
func (s *Store) ToggleCronJob(id int64, enabled bool) error {
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}
	_, err := s.db.Exec("UPDATE cron_jobs SET enabled = ? WHERE id = ?", enabledInt, id)
	return err
}

// GetModelOverride retrieves the model override for a chat session
func (s *Store) GetModelOverride(chatID int64) (string, error) {
	var modelOverride string
	err := s.db.QueryRow("SELECT model_override FROM sessions WHERE chat_id = ?", chatID).Scan(&modelOverride)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return modelOverride, err
}

// SetModelOverride sets the model override for a chat session
func (s *Store) SetModelOverride(chatID int64, model string) error {
	// Ensure session exists first
	var sessionID int64
	err := s.db.QueryRow("SELECT id FROM sessions WHERE chat_id = ?", chatID).Scan(&sessionID)
	if err == sql.ErrNoRows {
		// Create session if it doesn't exist
		_, err := s.db.Exec("INSERT INTO sessions (chat_id, state, model_override, queue_mode) VALUES (?, '', ?, ?)", chatID, model, defaultQueueMode)
		if err != nil {
			return err
		}
		_, err = s.syncSessionV2ByChatID(chatID)
		return err
	} else if err != nil {
		return err
	}

	// Update existing session
	_, err = s.db.Exec("UPDATE sessions SET model_override = ? WHERE chat_id = ?", model, chatID)
	if err != nil {
		return err
	}
	_, err = s.syncSessionV2ByChatID(chatID)
	return err
}

// AuthorizedUser represents an authorized user
type AuthorizedUser struct {
	ID           int64
	UserID       int64
	Username     string
	AuthorizedAt string
	PairedBy     string
}

// IsUserAuthorized checks if a user is authorized
func (s *Store) IsUserAuthorized(userID int64) bool {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM authorized_users WHERE user_id = ?", userID).Scan(&count)
	if err != nil {
		return false
	}
	return count > 0
}

// AuthorizeUser adds a user to the authorized users list
func (s *Store) AuthorizeUser(userID int64, username, method string) error {
	_, err := s.db.Exec(
		"INSERT INTO authorized_users (user_id, username, paired_by) VALUES (?, ?, ?) ON CONFLICT(user_id) DO UPDATE SET username = excluded.username, paired_by = excluded.paired_by, authorized_at = CURRENT_TIMESTAMP",
		userID, username, method,
	)
	return err
}

// DeauthorizeUser removes a user from the authorized users list
func (s *Store) DeauthorizeUser(userID int64) error {
	_, err := s.db.Exec("DELETE FROM authorized_users WHERE user_id = ?", userID)
	return err
}

// ListAuthorizedUsers returns all authorized users
func (s *Store) ListAuthorizedUsers() ([]AuthorizedUser, error) {
	rows, err := s.db.Query(`
		SELECT id, user_id, username, authorized_at, paired_by
		FROM authorized_users
		ORDER BY authorized_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []AuthorizedUser
	for rows.Next() {
		var user AuthorizedUser
		var username, pairedBy sql.NullString
		if err := rows.Scan(&user.ID, &user.UserID, &username, &user.AuthorizedAt, &pairedBy); err != nil {
			continue
		}
		if username.Valid {
			user.Username = username.String
		}
		if pairedBy.Valid {
			user.PairedBy = pairedBy.String
		}
		users = append(users, user)
	}

	return users, nil
}

// GetGroupMode retrieves the activation mode for a group
func (s *Store) GetGroupMode(chatID int64) (string, error) {
	var groupMode string
	err := s.db.QueryRow("SELECT group_mode FROM sessions WHERE chat_id = ?", chatID).Scan(&groupMode)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return groupMode, err
}

// SetGroupMode sets the activation mode for a group
func (s *Store) SetGroupMode(chatID int64, mode string) error {
	// Ensure session exists first
	var sessionID int64
	err := s.db.QueryRow("SELECT id FROM sessions WHERE chat_id = ?", chatID).Scan(&sessionID)
	if err == sql.ErrNoRows {
		// Create session if it doesn't exist
		_, err := s.db.Exec("INSERT INTO sessions (chat_id, state, group_mode, queue_mode) VALUES (?, '', ?, ?)", chatID, mode, defaultQueueMode)
		if err != nil {
			return err
		}
		_, err = s.syncSessionV2ByChatID(chatID)
		return err
	} else if err != nil {
		return err
	}

	// Update existing session
	_, err = s.db.Exec("UPDATE sessions SET group_mode = ? WHERE chat_id = ?", mode, chatID)
	if err != nil {
		return err
	}
	_, err = s.syncSessionV2ByChatID(chatID)
	return err
}

// GetActiveAgent retrieves the active agent name for a chat session
func (s *Store) GetActiveAgent(chatID int64) (string, error) {
	var activeAgent string
	err := s.db.QueryRow("SELECT active_agent FROM sessions WHERE chat_id = ?", chatID).Scan(&activeAgent)
	if err == sql.ErrNoRows {
		return "default", nil // Return default if no session exists
	}
	if err != nil {
		return "", err
	}
	if activeAgent == "" {
		return "default", nil
	}
	return activeAgent, nil
}

// TokenUsage holds token usage data for a session
type TokenUsage struct {
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	ContextTokens   int
	CompactionCount int
	MessageCount    int
	UpdatedAt       string
}

// UpdateTokenUsage adds token usage from an AI response to the session
func (s *Store) UpdateTokenUsage(chatID int64, promptTokens, completionTokens, totalTokens int) error {
	_, err := s.db.Exec(`
		INSERT INTO sessions (chat_id, state, input_tokens, output_tokens, total_tokens, queue_mode)
		VALUES (?, '', ?, ?, ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET
			input_tokens = input_tokens + excluded.input_tokens,
			output_tokens = output_tokens + excluded.output_tokens,
			total_tokens = excluded.total_tokens,
			updated_at = CURRENT_TIMESTAMP
	`, chatID, promptTokens, completionTokens, totalTokens, defaultQueueMode)
	if err != nil {
		return err
	}
	_, err = s.syncSessionV2ByChatID(chatID)
	return err
}

// SetContextTokens sets the context window limit for a session
func (s *Store) SetContextTokens(chatID int64, contextTokens int) error {
	_, err := s.db.Exec(`
		UPDATE sessions SET context_tokens = ? WHERE chat_id = ?
	`, contextTokens, chatID)
	if err != nil {
		return err
	}
	_, err = s.syncSessionV2ByChatID(chatID)
	return err
}

// GetTokenUsage retrieves token usage for a session
func (s *Store) GetTokenUsage(chatID int64) (*TokenUsage, error) {
	var u TokenUsage
	var updatedAt sql.NullString
	err := s.db.QueryRow(`
		SELECT input_tokens, output_tokens, total_tokens, context_tokens, compaction_count, message_count, updated_at
		FROM sessions WHERE chat_id = ?
	`, chatID).Scan(&u.InputTokens, &u.OutputTokens, &u.TotalTokens, &u.ContextTokens, &u.CompactionCount, &u.MessageCount, &updatedAt)
	if err == sql.ErrNoRows {
		return &TokenUsage{}, nil
	}
	if err != nil {
		return nil, err
	}
	if updatedAt.Valid {
		u.UpdatedAt = updatedAt.String
	}
	return &u, nil
}

// ResetSession clears session state and token counters for a new session
func (s *Store) ResetSession(chatID int64) error {
	_, err := s.db.Exec(`
		UPDATE sessions SET state = '', input_tokens = 0, output_tokens = 0, total_tokens = 0,
		message_count = 0, compaction_count = 0, last_summary = '', updated_at = CURRENT_TIMESTAMP
		WHERE chat_id = ?
	`, chatID)
	if err != nil {
		return err
	}

	// Clear session messages from both v1 and v2 tables.
	// Use both dm: and group: keys to cover all session key formats.
	if _, err := s.db.Exec(`DELETE FROM session_messages WHERE chat_id = ?`, chatID); err != nil {
		return err
	}
	dmKey := fmt.Sprintf("dm:%d", chatID)
	groupKey := fmt.Sprintf("group:%d", chatID)
	if _, err := s.db.Exec(`DELETE FROM session_messages_v2 WHERE session_key IN (?, ?)`, dmKey, groupKey); err != nil {
		return err
	}
	// Also try the v2 session key from sessions_v2 table.
	if sessionKey, err := s.syncSessionV2ByChatID(chatID); err == nil && sessionKey != "" && sessionKey != dmKey && sessionKey != groupKey {
		if _, err := s.db.Exec(`DELETE FROM session_messages_v2 WHERE session_key = ?`, sessionKey); err != nil {
			return err
		}
	}
	return nil
}

// IsEmergencyStopEnabled reports whether dangerous tool families are disabled.
// Missing state defaults to false so the bot starts in the trusted default mode.
func (s *Store) IsEmergencyStopEnabled() (bool, error) {
	var raw string
	err := s.db.QueryRow(`SELECT value FROM app_state WHERE key = ?`, emergencyStopStateKey).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "0", "false", "off":
		return false, nil
	case "1", "true", "on":
		return true, nil
	default:
		return false, fmt.Errorf("invalid estop state %q", raw)
	}
}

// SetEmergencyStopEnabled persists the global estop flag.
func (s *Store) SetEmergencyStopEnabled(enabled bool) error {
	value := "0"
	if enabled {
		value = "1"
	}

	_, err := s.db.Exec(`
		INSERT INTO app_state (key, value, updated_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = CURRENT_TIMESTAMP
	`, emergencyStopStateKey, value)
	return err
}

// GetSessionOption retrieves a string option from session
func (s *Store) GetSessionOption(chatID int64, column string) (string, error) {
	// Validate column name to prevent SQL injection
	validColumns := map[string]bool{
		"usage_mode": true, "think_level": true, "queue_mode": true,
	}
	if !validColumns[column] {
		return "", fmt.Errorf("invalid column: %s", column)
	}
	var val string
	err := s.db.QueryRow("SELECT "+column+" FROM sessions WHERE chat_id = ?", chatID).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetSessionOption sets a string option on a session
func (s *Store) SetSessionOption(chatID int64, column, value string) error {
	validColumns := map[string]bool{
		"usage_mode": true, "think_level": true, "queue_mode": true,
	}
	if !validColumns[column] {
		return fmt.Errorf("invalid column: %s", column)
	}
	// Ensure session exists
	_, err := s.db.Exec(`
		INSERT INTO sessions (chat_id, state, queue_mode) VALUES (?, '', ?)
		ON CONFLICT(chat_id) DO NOTHING
	`, chatID, defaultQueueMode)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("UPDATE sessions SET "+column+" = ? WHERE chat_id = ?", value, chatID)
	if err != nil {
		return err
	}
	_, err = s.syncSessionV2ByChatID(chatID)
	return err
}

// GetVerbose retrieves verbose flag for a session
func (s *Store) GetVerbose(chatID int64) (bool, error) {
	var v int
	err := s.db.QueryRow("SELECT verbose FROM sessions WHERE chat_id = ?", chatID).Scan(&v)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return v != 0, err
}

// SetVerbose sets verbose flag on a session
func (s *Store) SetVerbose(chatID int64, verbose bool) error {
	v := 0
	if verbose {
		v = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO sessions (chat_id, state, verbose, queue_mode) VALUES (?, '', ?, ?)
		ON CONFLICT(chat_id) DO UPDATE SET verbose = excluded.verbose
	`, chatID, v, defaultQueueMode)
	if err != nil {
		return err
	}
	_, err = s.syncSessionV2ByChatID(chatID)
	return err
}

// SetActiveAgent sets the active agent for a chat session
func (s *Store) SetActiveAgent(chatID int64, agentName string) error {
	// Ensure session exists first
	var sessionID int64
	err := s.db.QueryRow("SELECT id FROM sessions WHERE chat_id = ?", chatID).Scan(&sessionID)
	if err == sql.ErrNoRows {
		// Create session if it doesn't exist
		_, err := s.db.Exec("INSERT INTO sessions (chat_id, state, active_agent, queue_mode) VALUES (?, '', ?, ?)", chatID, agentName, defaultQueueMode)
		if err != nil {
			return err
		}
		_, err = s.syncSessionV2ByChatID(chatID)
		return err
	} else if err != nil {
		return err
	}

	// Update existing session
	_, err = s.db.Exec("UPDATE sessions SET active_agent = ? WHERE chat_id = ?", agentName, chatID)
	if err != nil {
		return err
	}
	_, err = s.syncSessionV2ByChatID(chatID)
	return err
}

// SessionRoute stores the latest outbound delivery metadata for a session key.
type SessionRoute struct {
	SessionKey       string
	Channel          string
	ChatID           int64
	ThreadID         int
	ReplyToMessageID int
	UserID           int64
	Username         string
	UpdatedAt        string
}

// SaveSessionRoute upserts the latest delivery route for a canonical session key.
func (s *Store) SaveSessionRoute(route SessionRoute) error {
	key := strings.TrimSpace(route.SessionKey)
	if key == "" {
		return fmt.Errorf("session key is required")
	}
	channel := strings.TrimSpace(route.Channel)
	if channel == "" {
		channel = defaultRouteTransport
	}

	_, err := s.db.Exec(`
		INSERT INTO session_routes (
			session_key, channel, chat_id, thread_id, reply_to_message_id, user_id, username
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
			channel = excluded.channel,
			chat_id = excluded.chat_id,
			thread_id = excluded.thread_id,
			reply_to_message_id = excluded.reply_to_message_id,
			user_id = excluded.user_id,
			username = excluded.username,
			updated_at = CURRENT_TIMESTAMP
	`, key, channel, route.ChatID, route.ThreadID, route.ReplyToMessageID, route.UserID, route.Username)
	return err
}

// GetSessionRoute loads the delivery route for the session key.
// It returns (nil, nil) when no route exists.
func (s *Store) GetSessionRoute(sessionKey string) (*SessionRoute, error) {
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return nil, nil
	}

	var route SessionRoute
	err := s.db.QueryRow(`
		SELECT session_key, channel, chat_id, thread_id, reply_to_message_id, user_id, username, updated_at
		FROM session_routes
		WHERE session_key = ?
	`, key).Scan(
		&route.SessionKey,
		&route.Channel,
		&route.ChatID,
		&route.ThreadID,
		&route.ReplyToMessageID,
		&route.UserID,
		&route.Username,
		&route.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &route, nil
}

// DeleteSessionRoute removes persisted delivery metadata for the session key.
func (s *Store) DeleteSessionRoute(sessionKey string) error {
	_, err := s.db.Exec("DELETE FROM session_routes WHERE session_key = ?", strings.TrimSpace(sessionKey))
	return err
}

// Job holds one durable background-work record.
type Job struct {
	JobID              string
	Kind               string
	Worker             string
	SessionKey         string
	DeliverySessionKey string
	RetryOfJobID       string
	Description        string
	Status             string
	CancelRequested    bool
	Attempt            int
	MaxAttempts        int
	TimeoutSeconds     int
	Summary            string
	Error              string
	CreatedAt          string
	StartedAt          string
	CompletedAt        string
	UpdatedAt          string
}

// JobEvent holds one persisted lifecycle event for a job.
type JobEvent struct {
	ID        int64
	JobID     string
	EventType string
	Message   string
	Payload   string
	CreatedAt string
}

// JobArtifact holds a durable output artifact emitted by a job.
type JobArtifact struct {
	ID           int64
	JobID        string
	Name         string
	ArtifactType string
	MimeType     string
	Content      string
	URI          string
	Metadata     string
	CreatedAt    string
}

// CreateJob inserts a new durable job row.
func (s *Store) CreateJob(job Job) error {
	jobID := strings.TrimSpace(job.JobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}
	kind := strings.TrimSpace(job.Kind)
	if kind == "" {
		return fmt.Errorf("job kind is required")
	}
	status := strings.TrimSpace(job.Status)
	if status == "" {
		status = "pending"
	}
	attempt := job.Attempt
	if attempt <= 0 {
		attempt = 1
	}
	maxAttempts := job.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	cancelRequested := 0
	if job.CancelRequested {
		cancelRequested = 1
	}

	_, err := s.db.Exec(`
		INSERT INTO jobs (
			job_id, kind, worker, session_key, delivery_session_key, retry_of_job_id,
			description, status, cancel_requested, attempt, max_attempts, timeout_seconds,
			summary, error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		jobID,
		kind,
		strings.TrimSpace(job.Worker),
		strings.TrimSpace(job.SessionKey),
		strings.TrimSpace(job.DeliverySessionKey),
		strings.TrimSpace(job.RetryOfJobID),
		job.Description,
		status,
		cancelRequested,
		attempt,
		maxAttempts,
		job.TimeoutSeconds,
		job.Summary,
		job.Error,
	)
	return err
}

// GetJob loads one durable job by ID.
// It returns (nil, nil) when the row does not exist.
func (s *Store) GetJob(jobID string) (*Job, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return nil, nil
	}

	var (
		job                Job
		cancelRequestedInt int
	)
	err := s.db.QueryRow(`
		SELECT job_id, kind, worker, session_key, delivery_session_key, retry_of_job_id,
		       description, status, cancel_requested, attempt, max_attempts, timeout_seconds,
		       summary, error, created_at, COALESCE(started_at, ''), COALESCE(completed_at, ''), updated_at
		FROM jobs
		WHERE job_id = ?
	`, jobID).Scan(
		&job.JobID,
		&job.Kind,
		&job.Worker,
		&job.SessionKey,
		&job.DeliverySessionKey,
		&job.RetryOfJobID,
		&job.Description,
		&job.Status,
		&cancelRequestedInt,
		&job.Attempt,
		&job.MaxAttempts,
		&job.TimeoutSeconds,
		&job.Summary,
		&job.Error,
		&job.CreatedAt,
		&job.StartedAt,
		&job.CompletedAt,
		&job.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	job.CancelRequested = cancelRequestedInt != 0
	return &job, nil
}

// ListJobs returns the newest durable jobs first.
func (s *Store) ListJobs(limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 50
	}

	rows, err := s.db.Query(`
		SELECT job_id, kind, worker, session_key, delivery_session_key, retry_of_job_id,
		       description, status, cancel_requested, attempt, max_attempts, timeout_seconds,
		       summary, error, created_at, COALESCE(started_at, ''), COALESCE(completed_at, ''), updated_at
		FROM jobs
		ORDER BY created_at DESC, job_id DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var (
			job                Job
			cancelRequestedInt int
		)
		if err := rows.Scan(
			&job.JobID,
			&job.Kind,
			&job.Worker,
			&job.SessionKey,
			&job.DeliverySessionKey,
			&job.RetryOfJobID,
			&job.Description,
			&job.Status,
			&cancelRequestedInt,
			&job.Attempt,
			&job.MaxAttempts,
			&job.TimeoutSeconds,
			&job.Summary,
			&job.Error,
			&job.CreatedAt,
			&job.StartedAt,
			&job.CompletedAt,
			&job.UpdatedAt,
		); err != nil {
			return nil, err
		}
		job.CancelRequested = cancelRequestedInt != 0
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// UpdateJobCancelRequested flips the cancel_requested flag for the job.
func (s *Store) UpdateJobCancelRequested(jobID string, requested bool) error {
	cancelRequested := 0
	if requested {
		cancelRequested = 1
	}
	_, err := s.db.Exec(`
		UPDATE jobs
		SET cancel_requested = ?, updated_at = CURRENT_TIMESTAMP
		WHERE job_id = ?
	`, cancelRequested, strings.TrimSpace(jobID))
	return err
}

// MarkJobRunning transitions the job into the running state.
func (s *Store) MarkJobRunning(jobID string) error {
	_, err := s.db.Exec(`
		UPDATE jobs
		SET status = 'running',
		    started_at = COALESCE(started_at, CURRENT_TIMESTAMP),
		    updated_at = CURRENT_TIMESTAMP
		WHERE job_id = ?
	`, strings.TrimSpace(jobID))
	return err
}

func (s *Store) markJobTerminal(jobID, status, summary, errMsg string) error {
	_, err := s.db.Exec(`
		UPDATE jobs
		SET status = ?,
		    summary = ?,
		    error = ?,
		    completed_at = CURRENT_TIMESTAMP,
		    updated_at = CURRENT_TIMESTAMP
		WHERE job_id = ?
	`, status, summary, errMsg, strings.TrimSpace(jobID))
	return err
}

// MarkJobSucceeded marks the job successful and stores the final summary.
func (s *Store) MarkJobSucceeded(jobID, summary string) error {
	return s.markJobTerminal(jobID, "succeeded", summary, "")
}

// MarkJobFailed marks the job failed and stores the error message.
func (s *Store) MarkJobFailed(jobID, errMsg string) error {
	return s.markJobTerminal(jobID, "failed", "", errMsg)
}

// MarkJobCancelled marks the job cancelled and stores the cancellation reason.
func (s *Store) MarkJobCancelled(jobID, errMsg string) error {
	return s.markJobTerminal(jobID, "cancelled", "", errMsg)
}

// MarkJobTimedOut marks the job timed out and stores the timeout reason.
func (s *Store) MarkJobTimedOut(jobID, errMsg string) error {
	return s.markJobTerminal(jobID, "timed_out", "", errMsg)
}

// AddJobEvent persists a lifecycle event for the job.
func (s *Store) AddJobEvent(event JobEvent) error {
	jobID := strings.TrimSpace(event.JobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}
	eventType := strings.TrimSpace(event.EventType)
	if eventType == "" {
		return fmt.Errorf("event type is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO job_events (job_id, event_type, message, payload)
		VALUES (?, ?, ?, ?)
	`, jobID, eventType, event.Message, event.Payload)
	return err
}

// ListJobEvents returns job lifecycle events in chronological order.
func (s *Store) ListJobEvents(jobID string, limit int) ([]JobEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT id, job_id, event_type, message, payload, created_at
		FROM (
			SELECT id, job_id, event_type, message, payload, created_at
			FROM job_events
			WHERE job_id = ?
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		)
		ORDER BY created_at ASC, id ASC
	`, strings.TrimSpace(jobID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []JobEvent
	for rows.Next() {
		var event JobEvent
		if err := rows.Scan(&event.ID, &event.JobID, &event.EventType, &event.Message, &event.Payload, &event.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// AddJobArtifact persists one durable job artifact.
func (s *Store) AddJobArtifact(artifact JobArtifact) error {
	jobID := strings.TrimSpace(artifact.JobID)
	if jobID == "" {
		return fmt.Errorf("job ID is required")
	}
	name := strings.TrimSpace(artifact.Name)
	if name == "" {
		return fmt.Errorf("artifact name is required")
	}
	artifactType := strings.TrimSpace(artifact.ArtifactType)
	if artifactType == "" {
		return fmt.Errorf("artifact type is required")
	}
	_, err := s.db.Exec(`
		INSERT INTO job_artifacts (job_id, name, artifact_type, mime_type, content, uri, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, jobID, name, artifactType, artifact.MimeType, artifact.Content, artifact.URI, artifact.Metadata)
	return err
}

// ListJobArtifacts returns job artifacts in chronological order.
func (s *Store) ListJobArtifacts(jobID string, limit int) ([]JobArtifact, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT id, job_id, name, artifact_type, mime_type, content, uri, metadata, created_at
		FROM (
			SELECT id, job_id, name, artifact_type, mime_type, content, uri, metadata, created_at
			FROM job_artifacts
			WHERE job_id = ?
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		)
		ORDER BY created_at ASC, id ASC
	`, strings.TrimSpace(jobID), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []JobArtifact
	for rows.Next() {
		var artifact JobArtifact
		if err := rows.Scan(
			&artifact.ID,
			&artifact.JobID,
			&artifact.Name,
			&artifact.ArtifactType,
			&artifact.MimeType,
			&artifact.Content,
			&artifact.URI,
			&artifact.Metadata,
			&artifact.CreatedAt,
		); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, rows.Err()
}

// ─── v2 session helpers ──────────────────────────────────────────────────────

// SessionV2 holds the data stored in sessions_v2.
type SessionV2 struct {
	SessionKey         string
	AgentID            string
	ParentSessionKey   string
	ModelOverride      string
	ThinkLevel         string
	ActiveAgent        string
	UsageMode          string
	Verbose            bool
	QueueMode          string
	QueueDebounceMs    int
	MessageCount       int
	InputTokens        int
	OutputTokens       int
	TotalTokens        int
	ContextTokens      int
	CompactionCount    int
	LastSummary        string
	PromotedFromChatID int64
	CreatedAt          string
	UpdatedAt          string
}

// GetSessionV2 retrieves the v2 session for the given session key.
// Returns nil (no error) when the session does not yet exist.
func (s *Store) GetSessionV2(sessionKey string) (*SessionV2, error) {
	var sess SessionV2
	var verbose int
	var lastSummary sql.NullString
	err := s.db.QueryRow(`
		SELECT session_key, agent_id, parent_session_key, model_override, think_level,
		       active_agent, usage_mode, verbose, queue_mode, queue_debounce_ms,
		       message_count, input_tokens, output_tokens, total_tokens, context_tokens,
		       compaction_count, last_summary, promoted_from_chat_id, created_at, updated_at
		FROM sessions_v2 WHERE session_key = ?
	`, sessionKey).Scan(
		&sess.SessionKey, &sess.AgentID, &sess.ParentSessionKey, &sess.ModelOverride, &sess.ThinkLevel,
		&sess.ActiveAgent, &sess.UsageMode, &verbose, &sess.QueueMode, &sess.QueueDebounceMs,
		&sess.MessageCount, &sess.InputTokens, &sess.OutputTokens, &sess.TotalTokens, &sess.ContextTokens,
		&sess.CompactionCount, &lastSummary, &sess.PromotedFromChatID, &sess.CreatedAt, &sess.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sess.Verbose = verbose != 0
	if lastSummary.Valid {
		sess.LastSummary = lastSummary.String
	}
	return &sess, nil
}

// UpsertSessionV2 creates or updates a v2 session record.
func (s *Store) UpsertSessionV2(sess *SessionV2) error {
	verbose := 0
	if sess.Verbose {
		verbose = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO sessions_v2
			(session_key, agent_id, parent_session_key, model_override, think_level,
			 active_agent, usage_mode, verbose, queue_mode, queue_debounce_ms,
			 message_count, input_tokens, output_tokens, total_tokens, context_tokens,
			 compaction_count, last_summary, promoted_from_chat_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
			agent_id             = excluded.agent_id,
			parent_session_key   = excluded.parent_session_key,
			model_override       = excluded.model_override,
			think_level          = excluded.think_level,
			active_agent         = excluded.active_agent,
			usage_mode           = excluded.usage_mode,
			verbose              = excluded.verbose,
			queue_mode           = excluded.queue_mode,
			queue_debounce_ms    = excluded.queue_debounce_ms,
			message_count        = excluded.message_count,
			input_tokens         = excluded.input_tokens,
			output_tokens        = excluded.output_tokens,
			total_tokens         = excluded.total_tokens,
			context_tokens       = excluded.context_tokens,
			compaction_count     = excluded.compaction_count,
			last_summary         = excluded.last_summary,
			promoted_from_chat_id = excluded.promoted_from_chat_id,
			updated_at           = CURRENT_TIMESTAMP
	`,
		sess.SessionKey, sess.AgentID, sess.ParentSessionKey, sess.ModelOverride, sess.ThinkLevel,
		sess.ActiveAgent, sess.UsageMode, verbose, sess.QueueMode, sess.QueueDebounceMs,
		sess.MessageCount, sess.InputTokens, sess.OutputTokens, sess.TotalTokens, sess.ContextTokens,
		sess.CompactionCount, sess.LastSummary, sess.PromotedFromChatID,
	)
	return err
}

// SessionMessageV2 holds one row from session_messages_v2.
type SessionMessageV2 struct {
	ID         int64
	SessionKey string
	Role       string
	Content    string
	RunID      string
	CreatedAt  string
}

// SaveSessionMessageV2 appends a message to the v2 transcript for sessionKey.
func (s *Store) SaveSessionMessageV2(sessionKey, role, content, runID string) error {
	_, err := s.db.Exec(`
		INSERT INTO session_messages_v2 (session_key, role, content, run_id)
		VALUES (?, ?, ?, ?)
	`, sessionKey, role, content, runID)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
		UPDATE sessions_v2 SET message_count = message_count + 1, updated_at = CURRENT_TIMESTAMP
		WHERE session_key = ?
	`, sessionKey)
	return err
}

// SaveSessionMessagePairV2 atomically persists a user+assistant message pair
// to the v2 transcript within a single transaction. This prevents orphaned
// user messages if the assistant write fails.
func (s *Store) SaveSessionMessagePairV2(sessionKey, userContent, assistantContent, runID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, msg := range []struct{ role, content string }{
		{"user", userContent},
		{"assistant", assistantContent},
	} {
		if _, err := tx.Exec(`
			INSERT INTO session_messages_v2 (session_key, role, content, run_id)
			VALUES (?, ?, ?, ?)
		`, sessionKey, msg.role, msg.content, runID); err != nil {
			return fmt.Errorf("insert %s message: %w", msg.role, err)
		}
	}
	if _, err := tx.Exec(`
		UPDATE sessions_v2 SET message_count = message_count + 2, updated_at = CURRENT_TIMESTAMP
		WHERE session_key = ?
	`, sessionKey); err != nil {
		return fmt.Errorf("update session counter: %w", err)
	}
	return tx.Commit()
}

// GetSessionMessagesV2 retrieves up to limit messages for sessionKey in
// chronological order (oldest first).
func (s *Store) GetSessionMessagesV2(sessionKey string, limit int) ([]SessionMessageV2, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, session_key, role, content, run_id, created_at
		FROM (
			SELECT id, session_key, role, content, run_id, created_at
			FROM session_messages_v2
			WHERE session_key = ?
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		) ORDER BY created_at ASC, id ASC
	`, sessionKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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

// ClearSessionMessagesV2 deletes all v2 transcript messages for a session
// and resets the message counter. Used by compaction to replace history with
// a compact summary.
func (s *Store) ClearSessionMessagesV2(sessionKey string) error {
	if _, err := s.db.Exec(`DELETE FROM session_messages_v2 WHERE session_key = ?`, sessionKey); err != nil {
		return err
	}
	_, err := s.db.Exec(`
		UPDATE sessions_v2 SET message_count = 0, updated_at = CURRENT_TIMESTAMP
		WHERE session_key = ?
	`, sessionKey)
	return err
}

// UpsertSessionRoute creates or updates the delivery route for sessionKey.
// Unlike SaveSessionRoute it performs no input validation and is suitable
// for internal callers that build the route inline.
func (s *Store) UpsertSessionRoute(route SessionRoute) error {
	_, err := s.db.Exec(`
		INSERT INTO session_routes (session_key, channel, chat_id, thread_id, reply_to_message_id, user_id, username)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_key) DO UPDATE SET
			channel              = excluded.channel,
			chat_id              = excluded.chat_id,
			thread_id            = excluded.thread_id,
			reply_to_message_id  = excluded.reply_to_message_id,
			user_id              = excluded.user_id,
			username             = excluded.username,
			updated_at           = CURRENT_TIMESTAMP
	`, route.SessionKey, route.Channel, route.ChatID, route.ThreadID, route.ReplyToMessageID, route.UserID, route.Username)
	return err
}

// PromoteLegacySession ensures a v2 session exists for sessionKey.
//
// If a v2 row already exists the call is a no-op (idempotent). Otherwise:
//   - A new sessions_v2 row is created using metadata copied from the legacy
//     sessions row that corresponds to chatID (if one exists).
//   - All legacy session_messages for chatID are copied (not deleted) into
//     session_messages_v2.
//   - A session_routes row is created/updated for the channel + chatID.
//
// Callers should invoke this on the first inbound message for a session_key
// that was previously known only by its raw chat_id.
func (s *Store) PromoteLegacySession(sessionKey, agentID, channel string, chatID int64) error {
	// Fast-path: v2 session already exists.
	existing, err := s.GetSessionV2(sessionKey)
	if err != nil {
		return fmt.Errorf("promote: check existing: %w", err)
	}
	if existing != nil {
		return nil
	}

	// Try to read legacy metadata for this chatID.
	sess := &SessionV2{
		SessionKey:         sessionKey,
		AgentID:            agentID,
		ActiveAgent:        "default",
		QueueMode:          defaultQueueMode,
		QueueDebounceMs:    1500,
		PromotedFromChatID: 0,
	}

	var (
		verbose     int
		lastSummary sql.NullString
	)
	legacyErr := s.db.QueryRow(`
		SELECT model_override, think_level, active_agent, usage_mode, verbose,
		       queue_mode, queue_debounce_ms, message_count,
		       input_tokens, output_tokens, total_tokens, context_tokens,
		       compaction_count, last_summary
		FROM sessions WHERE chat_id = ?
	`, chatID).Scan(
		&sess.ModelOverride, &sess.ThinkLevel, &sess.ActiveAgent, &sess.UsageMode, &verbose,
		&sess.QueueMode, &sess.QueueDebounceMs, &sess.MessageCount,
		&sess.InputTokens, &sess.OutputTokens, &sess.TotalTokens, &sess.ContextTokens,
		&sess.CompactionCount, &lastSummary,
	)
	if legacyErr != nil && legacyErr != sql.ErrNoRows {
		return fmt.Errorf("promote: read legacy session: %w", legacyErr)
	}
	if legacyErr == nil {
		sess.PromotedFromChatID = chatID
		sess.Verbose = verbose != 0
		if lastSummary.Valid {
			sess.LastSummary = lastSummary.String
		}
	}

	// Insert the v2 session row.
	if err := s.UpsertSessionV2(sess); err != nil {
		return fmt.Errorf("promote: upsert sessions_v2: %w", err)
	}

	// Copy legacy messages (if any) into session_messages_v2.
	// We collect all rows first, then insert, to avoid holding an open cursor
	// while performing write operations on the same SQLite connection.
	if sess.PromotedFromChatID != 0 {
		type legacyMsg struct{ role, content, createdAt string }
		var legacyMsgs []legacyMsg

		rows, err := s.db.Query(`
			SELECT sm.role, sm.content, sm.created_at
			FROM session_messages sm
			WHERE sm.chat_id = ?
			ORDER BY sm.created_at ASC, sm.id ASC
		`, chatID)
		if err != nil {
			return fmt.Errorf("promote: query legacy messages: %w", err)
		}
		for rows.Next() {
			var m legacyMsg
			if err := rows.Scan(&m.role, &m.content, &m.createdAt); err != nil {
				rows.Close()
				return fmt.Errorf("promote: scan legacy message: %w", err)
			}
			legacyMsgs = append(legacyMsgs, m)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("promote: iterate legacy messages: %w", err)
		}

		for _, m := range legacyMsgs {
			if _, err := s.db.Exec(`
				INSERT INTO session_messages_v2 (session_key, role, content, created_at)
				VALUES (?, ?, ?, ?)
			`, sessionKey, m.role, m.content, m.createdAt); err != nil {
				return fmt.Errorf("promote: insert session_messages_v2: %w", err)
			}
		}
	}

	// Record the delivery route.
	if err := s.UpsertSessionRoute(SessionRoute{
		SessionKey: sessionKey,
		Channel:    channel,
		ChatID:     chatID,
	}); err != nil {
		return fmt.Errorf("promote: upsert session_routes: %w", err)
	}

	return nil
}

// SubagentRun holds a persisted record of a sub-agent run.
type SubagentRun struct {
	ID               int64
	RunSlug          string
	SessionKey       string
	ParentSessionKey string
	AgentID          string
	Task             string
	Model            string
	Thinking         string
	ToolAllowlist    string // comma-separated
	WorkspaceRoot    string
	DeliverBack      bool
	Status           string // "pending", "running", "done", "error"
	Result           string
	Error            string
	SpawnedAt        string
	CompletedAt      string
}

// RecordSubagentSpawn inserts a new subagent_runs row with status "pending".
// toolAllowlist should be a comma-separated string of allowed tool names.
func (s *Store) RecordSubagentSpawn(runSlug, sessionKey, parentSessionKey, agentID, task, model, thinking, toolAllowlist, workspaceRoot string, deliverBack bool) error {
	agentID = normalizeAgentID(agentID)
	if err := s.ensureSessionV2(parentSessionKey, agentID, ""); err != nil {
		return err
	}
	if err := s.ensureSessionV2(sessionKey, agentID, parentSessionKey); err != nil {
		return err
	}

	deliverBackInt := 0
	if deliverBack {
		deliverBackInt = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO subagent_runs
			(run_id, run_slug, session_key, child_session_key, parent_session_key, agent_id, task, model, thinking, tool_allowlist, workspace_root, deliver_back, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending')
	`, runSlug, runSlug, sessionKey, sessionKey, parentSessionKey, agentID, task, model, thinking, toolAllowlist, workspaceRoot, deliverBackInt)
	return err
}

// UpdateSubagentStatus updates the status, result, and error of a subagent run.
// Pass status="running" when the run starts; "done" or "error" on completion.
// completedAt is ignored for non-terminal statuses.
func (s *Store) UpdateSubagentStatus(runSlug, status, result, errMsg string) error {
	switch status {
	case "done", "error":
		_, err := s.db.Exec(`
			UPDATE subagent_runs
			SET status = ?, result = ?, error = ?, completed_at = CURRENT_TIMESTAMP
			WHERE run_slug = ?
		`, status, result, errMsg, runSlug)
		return err
	default:
		_, err := s.db.Exec(`
			UPDATE subagent_runs SET status = ? WHERE run_slug = ?
		`, status, runSlug)
		return err
	}
}

// GetSubagentRuns returns subagent runs for a given parent session key, newest first.
func (s *Store) GetSubagentRuns(parentSessionKey string, limit int) ([]SubagentRun, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, run_slug, session_key, parent_session_key, agent_id,
		       task, model, thinking, tool_allowlist, workspace_root,
		       deliver_back, status, result, error, spawned_at,
		       COALESCE(completed_at, '') AS completed_at
		FROM subagent_runs
		WHERE parent_session_key = ?
		ORDER BY spawned_at DESC
		LIMIT ?
	`, parentSessionKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []SubagentRun
	for rows.Next() {
		var r SubagentRun
		var deliverBackInt int
		if err := rows.Scan(
			&r.ID, &r.RunSlug, &r.SessionKey, &r.ParentSessionKey, &r.AgentID,
			&r.Task, &r.Model, &r.Thinking, &r.ToolAllowlist, &r.WorkspaceRoot,
			&deliverBackInt, &r.Status, &r.Result, &r.Error, &r.SpawnedAt, &r.CompletedAt,
		); err != nil {
			return nil, err
		}
		r.DeliverBack = deliverBackInt != 0
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// RecentUserMessages returns the most recent user messages across all sessions,
// useful for analyzing chat patterns. Only "user" role messages are returned.
func (s *Store) RecentUserMessages(limit int) ([]SessionMessageV2, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT id, session_key, role, content, COALESCE(run_id, ''), created_at
		FROM session_messages_v2
		WHERE role = 'user'
		ORDER BY created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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

// RecentCompletedJobs returns recently completed (succeeded/failed) durable jobs.
func (s *Store) RecentCompletedJobs(limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT job_id, kind, COALESCE(worker, ''), COALESCE(session_key, ''),
		       COALESCE(delivery_session_key, ''), COALESCE(retry_of_job_id, ''),
		       COALESCE(description, ''), status, cancel_requested,
		       attempt, max_attempts, COALESCE(timeout_seconds, 0),
		       COALESCE(summary, ''), COALESCE(error, ''),
		       COALESCE(created_at, ''), COALESCE(started_at, ''),
		       COALESCE(completed_at, ''), COALESCE(updated_at, '')
		FROM jobs
		WHERE status IN ('succeeded', 'failed')
		ORDER BY completed_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var j Job
		var cancelInt int
		if err := rows.Scan(
			&j.JobID, &j.Kind, &j.Worker, &j.SessionKey,
			&j.DeliverySessionKey, &j.RetryOfJobID,
			&j.Description, &j.Status, &cancelInt,
			&j.Attempt, &j.MaxAttempts, &j.TimeoutSeconds,
			&j.Summary, &j.Error,
			&j.CreatedAt, &j.StartedAt, &j.CompletedAt, &j.UpdatedAt,
		); err != nil {
			return nil, err
		}
		j.CancelRequested = cancelInt != 0
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// AllCronJobs returns all cron jobs (both enabled and disabled).
func (s *Store) AllCronJobs() ([]CronJob, error) {
	rows, err := s.db.Query(`
		SELECT id, expression, task, chat_id, COALESCE(next_run, ''), enabled, created_at,
		       COALESCE(type, 'llm'), COALESCE(timeout_seconds, 0)
		FROM cron_jobs
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var jobs []CronJob
	for rows.Next() {
		var job CronJob
		if err := rows.Scan(&job.ID, &job.Expression, &job.Task, &job.ChatID, &job.NextRun, &job.Enabled, &job.CreatedAt, &job.Type, &job.TimeoutSeconds); err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}
