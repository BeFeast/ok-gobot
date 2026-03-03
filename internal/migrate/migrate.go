// Package migrate provides one-shot migration tooling from OpenClaw to ok-gobot.
//
// OpenClaw is the predecessor Python-based Telegram bot. Its SQLite database
// stores sessions keyed by chat_id (integer). ok-gobot uses the same chat_id
// column but maps sessions to canonical string keys at runtime:
//
//	agent:<agentId>:telegram:group:<chatId>
//	agent:<agentId>:telegram:dm:<userId>
//
// The migrator:
//  1. Reads OpenClaw sessions and message history from the source DB.
//  2. Creates a timestamped backup of the target (gobot) DB.
//  3. Inserts sessions and messages into the target DB, skipping conflicts.
//  4. Optionally copies workspace files (personality/soul files) from the
//     OpenClaw workspace directory into the gobot soul path.
//  5. Emits a canonical-key mapping report for rollback awareness.
package migrate

import (
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Options controls migration behaviour.
type Options struct {
	// SourceDB is the path to the OpenClaw SQLite database (required).
	SourceDB string
	// TargetDB is the path to the ok-gobot SQLite database.
	// Defaults to the gobot storage_path when empty.
	TargetDB string
	// SourceWorkspace is an optional path to the OpenClaw workspace directory
	// (personality/soul files). Ignored when empty.
	SourceWorkspace string
	// TargetWorkspace is the destination for workspace files. Ignored when
	// SourceWorkspace is empty.
	TargetWorkspace string
	// AgentID is the gobot agent identifier used when building canonical
	// session keys. Defaults to "default".
	AgentID string
	// DryRun prints the planned actions without modifying any files.
	DryRun bool
	// BackupDir is the directory where the target DB backup is written.
	// Defaults to <TargetDB>-backups/ when empty.
	BackupDir string
}

// Action represents a single planned or completed migration step.
type Action struct {
	Kind    string // "session", "message", "workspace_file"
	Summary string
}

// Report is the result of a migration run.
type Report struct {
	BackupPath       string
	Actions          []Action
	SessionsTotal    int
	SessionsMigrated int
	SessionsSkipped  int
	MessagesTotal    int
	MessagesMigrated int
	MessagesSkipped  int
	WorkspaceFiles   int
	KeyMapping       []KeyMapping
	Errors           []string
}

// KeyMapping shows how an OpenClaw chat_id maps to a gobot canonical session key.
type KeyMapping struct {
	ChatID       int64
	ChatType     string // "private" or "group"
	CanonicalKey string
}

// sourceSession holds a row from the OpenClaw sessions table.
type sourceSession struct {
	ChatID          int64
	State           string
	MessageCount    int
	LastSummary     string
	CompactionCount int
	ModelOverride   string
	GroupMode       string
	ActiveAgent     string
	InputTokens     int
	OutputTokens    int
	TotalTokens     int
	ContextTokens   int
	UsageMode       string
	ThinkLevel      string
	Verbose         int
	UpdatedAt       string
}

// sourceMessage holds a row from the OpenClaw session_messages table.
type sourceMessage struct {
	ChatID    int64
	Role      string
	Content   string
	CreatedAt string
}

// Run executes the migration according to opts and returns a Report.
// In dry-run mode no writes are performed.
func Run(opts Options) (*Report, error) {
	if opts.SourceDB == "" {
		return nil, fmt.Errorf("migrate: source database path is required")
	}
	if opts.SourceWorkspace != "" && opts.TargetWorkspace == "" {
		return nil, fmt.Errorf("migrate: target workspace path is required when source workspace is set")
	}
	if opts.AgentID == "" {
		opts.AgentID = "default"
	}

	report := &Report{}

	// --- Open source database (read-only) ---
	srcDB, err := sql.Open("sqlite3", "file:"+opts.SourceDB+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("migrate: open source DB: %w", err)
	}
	defer srcDB.Close()

	if err := srcDB.Ping(); err != nil {
		return nil, fmt.Errorf("migrate: cannot access source DB %q: %w", opts.SourceDB, err)
	}

	// --- Validate source schema ---
	if err := validateSourceSchema(srcDB); err != nil {
		return nil, fmt.Errorf("migrate: source schema validation: %w", err)
	}

	// --- Read sessions from source ---
	sessions, err := readSessions(srcDB)
	if err != nil {
		return nil, fmt.Errorf("migrate: read sessions: %w", err)
	}
	report.SessionsTotal = len(sessions)

	// Build key mapping
	for _, s := range sessions {
		km := KeyMapping{
			ChatID:       s.ChatID,
			ChatType:     chatType(s.ChatID),
			CanonicalKey: canonicalKey(opts.AgentID, s.ChatID),
		}
		report.KeyMapping = append(report.KeyMapping, km)
	}

	// --- Read messages from source ---
	messages, err := readMessages(srcDB)
	if err != nil {
		return nil, fmt.Errorf("migrate: read messages: %w", err)
	}
	report.MessagesTotal = len(messages)

	// --- Collect workspace file list ---
	var workspaceFiles []string
	if opts.SourceWorkspace != "" && opts.TargetWorkspace != "" {
		workspaceFiles, err = collectWorkspaceFiles(opts.SourceWorkspace)
		if err != nil {
			return nil, fmt.Errorf("migrate: collect workspace files: %w", err)
		}
		report.WorkspaceFiles = len(workspaceFiles)
	}

	// --- Plan actions (always, even in dry-run) ---
	for _, s := range sessions {
		report.Actions = append(report.Actions, Action{
			Kind:    "session",
			Summary: fmt.Sprintf("import session chat_id=%d → %s (messages: %d)", s.ChatID, canonicalKey(opts.AgentID, s.ChatID), s.MessageCount),
		})
	}
	for _, m := range messages {
		report.Actions = append(report.Actions, Action{
			Kind:    "message",
			Summary: fmt.Sprintf("import message chat_id=%d role=%s (%d chars)", m.ChatID, m.Role, len(m.Content)),
		})
	}
	for _, f := range workspaceFiles {
		rel, _ := filepath.Rel(opts.SourceWorkspace, f)
		report.Actions = append(report.Actions, Action{
			Kind:    "workspace_file",
			Summary: fmt.Sprintf("copy workspace file %s", rel),
		})
	}

	if opts.DryRun {
		// Dry-run: report planned actions, no writes.
		report.SessionsMigrated = report.SessionsTotal
		report.MessagesMigrated = report.MessagesTotal
		return report, nil
	}

	// --- Backup target DB ---
	if opts.TargetDB != "" {
		backupPath, err := backupDB(opts.TargetDB, opts.BackupDir)
		if err != nil {
			return nil, fmt.Errorf("migrate: backup target DB: %w", err)
		}
		report.BackupPath = backupPath
	}

	// --- Open target database (read-write) ---
	if opts.TargetDB == "" {
		return nil, fmt.Errorf("migrate: target database path is required for apply mode")
	}

	dstDB, err := sql.Open("sqlite3", opts.TargetDB)
	if err != nil {
		return nil, fmt.Errorf("migrate: open target DB: %w", err)
	}
	defer dstDB.Close()

	if err := dstDB.Ping(); err != nil {
		return nil, fmt.Errorf("migrate: cannot access target DB %q: %w", opts.TargetDB, err)
	}

	// Ensure target schema is compatible.
	if err := ensureTargetSchema(dstDB); err != nil {
		return nil, fmt.Errorf("migrate: ensure target schema: %w", err)
	}

	// --- Migrate sessions ---
	for _, s := range sessions {
		inserted, err := insertSession(dstDB, s)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("session chat_id=%d: %v", s.ChatID, err))
			continue
		}
		if inserted {
			report.SessionsMigrated++
		} else {
			report.SessionsSkipped++
		}
	}

	// --- Migrate messages ---
	for _, m := range messages {
		inserted, err := insertMessage(dstDB, m)
		if err != nil {
			report.Errors = append(report.Errors, fmt.Sprintf("message chat_id=%d role=%s: %v", m.ChatID, m.Role, err))
			continue
		}
		if inserted {
			report.MessagesMigrated++
		} else {
			report.MessagesSkipped++
		}
	}

	// --- Copy workspace files ---
	if opts.SourceWorkspace != "" && opts.TargetWorkspace != "" {
		for _, srcFile := range workspaceFiles {
			rel, _ := filepath.Rel(opts.SourceWorkspace, srcFile)
			dstFile := filepath.Join(opts.TargetWorkspace, rel)
			if err := copyFile(srcFile, dstFile); err != nil {
				report.Errors = append(report.Errors, fmt.Sprintf("workspace file %s: %v", rel, err))
			}
		}
	}

	return report, nil
}

// validateSourceSchema checks that the source DB contains the expected tables.
// It is lenient: tables may be missing if the OpenClaw installation was minimal.
func validateSourceSchema(db *sql.DB) error {
	tables, err := tableNames(db)
	if err != nil {
		return err
	}
	set := make(map[string]bool, len(tables))
	for _, t := range tables {
		set[t] = true
	}
	if !set["sessions"] {
		return fmt.Errorf("source database has no 'sessions' table — is this an OpenClaw database?")
	}
	return nil
}

// tableNames returns the list of table names in db.
func tableNames(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// columnNames returns the column names of table in db.
func columnNames(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

// readSessions reads all sessions from the source DB, tolerating missing columns
// and NULL values.
func readSessions(db *sql.DB) ([]sourceSession, error) {
	cols, err := columnNames(db, "sessions")
	if err != nil {
		return nil, err
	}

	// Build SELECT list from available columns. Nullable text columns are wrapped
	// in COALESCE so we always get a non-NULL string back.
	baseFields := []string{"chat_id"}
	optFields := []struct {
		col string
		def string
	}{
		{"state", "''"},
		{"message_count", "0"},
		{"last_summary", "''"},
		{"compaction_count", "0"},
		{"model_override", "''"},
		{"group_mode", "''"},
		{"active_agent", "''"},
		{"input_tokens", "0"},
		{"output_tokens", "0"},
		{"total_tokens", "0"},
		{"context_tokens", "0"},
		{"usage_mode", "''"},
		{"think_level", "''"},
		{"verbose", "0"},
		{"updated_at", "CURRENT_TIMESTAMP"},
	}

	// Columns that may contain NULLs in practice and need COALESCE.
	nullableText := map[string]bool{
		"state": true, "last_summary": true, "model_override": true,
		"group_mode": true, "active_agent": true, "usage_mode": true,
		"think_level": true, "updated_at": true,
	}

	selectParts := make([]string, 0, len(baseFields)+len(optFields))
	for _, f := range baseFields {
		selectParts = append(selectParts, f)
	}
	for _, f := range optFields {
		if cols[f.col] {
			if nullableText[f.col] {
				selectParts = append(selectParts, fmt.Sprintf("COALESCE(%s, %s) AS %s", f.col, f.def, f.col))
			} else {
				selectParts = append(selectParts, f.col)
			}
		} else {
			selectParts = append(selectParts, f.def+" AS "+f.col)
		}
	}

	query := "SELECT " + strings.Join(selectParts, ", ") + " FROM sessions ORDER BY chat_id"
	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("query sessions: %w", err)
	}
	defer rows.Close()

	var sessions []sourceSession
	for rows.Next() {
		var s sourceSession
		if err := rows.Scan(
			&s.ChatID,
			&s.State,
			&s.MessageCount,
			&s.LastSummary,
			&s.CompactionCount,
			&s.ModelOverride,
			&s.GroupMode,
			&s.ActiveAgent,
			&s.InputTokens,
			&s.OutputTokens,
			&s.TotalTokens,
			&s.ContextTokens,
			&s.UsageMode,
			&s.ThinkLevel,
			&s.Verbose,
			&s.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// readMessages reads session_messages if the table exists.
func readMessages(db *sql.DB) ([]sourceMessage, error) {
	tables, err := tableNames(db)
	if err != nil {
		return nil, err
	}
	hasTable := false
	for _, t := range tables {
		if t == "session_messages" {
			hasTable = true
			break
		}
	}
	if !hasTable {
		return nil, nil
	}

	rows, err := db.Query(`
		SELECT chat_id, role, content,
		       COALESCE(created_at, CURRENT_TIMESTAMP) AS created_at
		FROM session_messages
		ORDER BY chat_id, id
	`)
	if err != nil {
		return nil, fmt.Errorf("query session_messages: %w", err)
	}
	defer rows.Close()

	var msgs []sourceMessage
	for rows.Next() {
		var m sourceMessage
		if err := rows.Scan(&m.ChatID, &m.Role, &m.Content, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message row: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// ensureTargetSchema creates required tables in the target DB if absent.
func ensureTargetSchema(db *sql.DB) error {
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER UNIQUE NOT NULL,
			state TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_chat_id ON sessions(chat_id)`,
		`CREATE TABLE IF NOT EXISTS session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_session_messages_chat ON session_messages(chat_id)`,
	}
	for _, stmt := range ddl {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	return nil
}

// insertSession inserts a session into the target DB.
// Returns (true, nil) when inserted, (false, nil) when skipped (already exists).
func insertSession(db *sql.DB, s sourceSession) (bool, error) {
	// Check for existing session.
	var existing int
	err := db.QueryRow("SELECT COUNT(*) FROM sessions WHERE chat_id=?", s.ChatID).Scan(&existing)
	if err != nil {
		return false, err
	}
	if existing > 0 {
		return false, nil
	}

	_, err = db.Exec(`
		INSERT INTO sessions (
			chat_id, state, message_count, last_summary, compaction_count,
			model_override, group_mode, active_agent,
			input_tokens, output_tokens, total_tokens, context_tokens,
			usage_mode, think_level, verbose, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(chat_id) DO NOTHING
	`,
		s.ChatID, s.State, s.MessageCount, s.LastSummary, s.CompactionCount,
		s.ModelOverride, s.GroupMode, s.ActiveAgent,
		s.InputTokens, s.OutputTokens, s.TotalTokens, s.ContextTokens,
		s.UsageMode, s.ThinkLevel, s.Verbose, s.UpdatedAt,
	)
	if err != nil {
		// May fail if target schema has fewer columns; fall back to minimal insert.
		_, err2 := db.Exec(
			`INSERT INTO sessions (chat_id, state, updated_at) VALUES (?,?,?) ON CONFLICT(chat_id) DO NOTHING`,
			s.ChatID, s.State, s.UpdatedAt,
		)
		if err2 != nil {
			return false, err2
		}
	}
	return true, nil
}

// insertMessage inserts a session message into the target DB.
// Returns (true, nil) when inserted, (false, nil) when the session doesn't
// exist or the message is already present from a prior migration run.
func insertMessage(db *sql.DB, m sourceMessage) (bool, error) {
	// Resolve session id.
	var sessionID int64
	err := db.QueryRow("SELECT id FROM sessions WHERE chat_id=?", m.ChatID).Scan(&sessionID)
	if err == sql.ErrNoRows {
		return false, nil // session not migrated, skip
	}
	if err != nil {
		return false, err
	}

	var existing int
	err = db.QueryRow(`
		SELECT COUNT(*)
		FROM session_messages
		WHERE session_id = ? AND chat_id = ? AND role = ? AND content = ? AND created_at = ?
	`, sessionID, m.ChatID, m.Role, m.Content, m.CreatedAt).Scan(&existing)
	if err != nil {
		return false, err
	}
	if existing > 0 {
		return false, nil
	}


	_, err = db.Exec(`
		INSERT INTO session_messages (session_id, chat_id, role, content, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, sessionID, m.ChatID, m.Role, m.Content, m.CreatedAt)
	if err != nil {
		return false, err
	}
	return true, nil
}

// backupDB copies the target DB file to BackupDir and returns the backup path.
func backupDB(targetDB, backupDir string) (string, error) {
	if _, err := os.Stat(targetDB); os.IsNotExist(err) {
		// Nothing to back up yet.
		return "", nil
	}
	if backupDir == "" {
		backupDir = filepath.Dir(targetDB) + "/backups"
	}
	if err := os.MkdirAll(backupDir, 0750); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	ts := time.Now().UTC().Format("20060102-150405")
	base := filepath.Base(targetDB)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	backupName := fmt.Sprintf("%s-%s%s", stem, ts, ext)
	backupPath := filepath.Join(backupDir, backupName)

	if err := copyFile(targetDB, backupPath); err != nil {
		return "", fmt.Errorf("copy to backup: %w", err)
	}
	return backupPath, nil
}

// collectWorkspaceFiles returns all regular files under root.
func collectWorkspaceFiles(root string) ([]string, error) {
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// copyFile copies src to dst, creating parent directories as needed.
// Existing files at dst are NOT overwritten (backup semantics: first write wins).
func copyFile(src, dst string) error {
	if _, err := os.Stat(dst); err == nil {
		return nil // already exists, skip
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0750); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0640)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// canonicalKey returns the gobot canonical session key for a chat_id.
// Negative chat_ids are Telegram groups; positive IDs are users (DMs).
func canonicalKey(agentID string, chatID int64) string {
	if chatID < 0 {
		return fmt.Sprintf("agent:%s:telegram:group:%d", agentID, chatID)
	}
	return fmt.Sprintf("agent:%s:telegram:dm:%d", agentID, chatID)
}

// chatType returns "group" or "private" based on the sign of chatID.
func chatType(chatID int64) string {
	if chatID < 0 {
		return "group"
	}
	return "private"
}
