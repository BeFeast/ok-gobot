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

	return nil
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
		"INSERT INTO sessions (chat_id, state) VALUES (?, ?) ON CONFLICT(chat_id) DO UPDATE SET state = excluded.state, updated_at = CURRENT_TIMESTAMP",
		chatID, state,
	)
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
		result, err := s.db.Exec("INSERT INTO sessions (chat_id, state) VALUES (?, '')", chatID)
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
	return err
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
	return err
}

// CronJob represents a scheduled task
type CronJob struct {
	ID         int64
	Expression string
	Task       string
	ChatID     int64
	NextRun    string
	Enabled    bool
	CreatedAt  string
}

// SaveCronJob creates or updates a cron job
func (s *Store) SaveCronJob(expression, task string, chatID int64) (int64, error) {
	result, err := s.db.Exec(
		"INSERT INTO cron_jobs (expression, task, chat_id) VALUES (?, ?, ?)",
		expression, task, chatID,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// GetCronJobs returns all enabled cron jobs
func (s *Store) GetCronJobs() ([]CronJob, error) {
	rows, err := s.db.Query(`
		SELECT id, expression, task, chat_id, next_run, enabled, created_at 
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
		if err := rows.Scan(&job.ID, &job.Expression, &job.Task, &job.ChatID, &nextRun, &job.Enabled, &job.CreatedAt); err != nil {
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
		_, err := s.db.Exec("INSERT INTO sessions (chat_id, state, model_override) VALUES (?, '', ?)", chatID, model)
		return err
	} else if err != nil {
		return err
	}

	// Update existing session
	_, err = s.db.Exec("UPDATE sessions SET model_override = ? WHERE chat_id = ?", model, chatID)
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
		_, err := s.db.Exec("INSERT INTO sessions (chat_id, state, group_mode) VALUES (?, '', ?)", chatID, mode)
		return err
	} else if err != nil {
		return err
	}

	// Update existing session
	_, err = s.db.Exec("UPDATE sessions SET group_mode = ? WHERE chat_id = ?", mode, chatID)
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

// SetActiveAgent sets the active agent for a chat session
func (s *Store) SetActiveAgent(chatID int64, agentName string) error {
	// Ensure session exists first
	var sessionID int64
	err := s.db.QueryRow("SELECT id FROM sessions WHERE chat_id = ?", chatID).Scan(&sessionID)
	if err == sql.ErrNoRows {
		// Create session if it doesn't exist
		_, err := s.db.Exec("INSERT INTO sessions (chat_id, state, active_agent) VALUES (?, '', ?)", chatID, agentName)
		return err
	} else if err != nil {
		return err
	}

	// Update existing session
	_, err = s.db.Exec("UPDATE sessions SET active_agent = ? WHERE chat_id = ?", agentName, chatID)
	return err
}
