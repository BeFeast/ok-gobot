package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

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
	}

	for _, migration := range migrations {
		if _, err := s.db.Exec(migration); err != nil {
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
