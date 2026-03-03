package migrate_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"ok-gobot/internal/migrate"
)

// createSourceDB creates a minimal OpenClaw-like SQLite database in dir and
// returns its path.
func createSourceDB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "openclaw.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	defer db.Close()

	_, err = db.Exec(`CREATE TABLE sessions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		chat_id INTEGER UNIQUE NOT NULL,
		state TEXT,
		message_count INTEGER DEFAULT 0,
		last_summary TEXT,
		compaction_count INTEGER DEFAULT 0,
		model_override TEXT DEFAULT '',
		group_mode TEXT DEFAULT '',
		active_agent TEXT DEFAULT '',
		input_tokens INTEGER DEFAULT 0,
		output_tokens INTEGER DEFAULT 0,
		total_tokens INTEGER DEFAULT 0,
		context_tokens INTEGER DEFAULT 0,
		usage_mode TEXT DEFAULT '',
		think_level TEXT DEFAULT '',
		verbose INTEGER DEFAULT 0,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create sessions: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE session_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id INTEGER,
		chat_id INTEGER NOT NULL,
		role TEXT NOT NULL,
		content TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatalf("create session_messages: %v", err)
	}

	// Insert two sessions: one group (negative chat_id), one private.
	_, err = db.Exec(`INSERT INTO sessions (chat_id, state, message_count) VALUES (-100123456, 'active', 5)`)
	if err != nil {
		t.Fatalf("insert group session: %v", err)
	}
	_, err = db.Exec(`INSERT INTO sessions (chat_id, state, message_count) VALUES (987654321, 'active', 3)`)
	if err != nil {
		t.Fatalf("insert dm session: %v", err)
	}

	// Insert messages for the group session.
	for _, m := range []struct{ role, content string }{
		{"user", "hello from openclaw"},
		{"assistant", "hi there"},
	} {
		_, err = db.Exec(`
			INSERT INTO session_messages (chat_id, role, content)
			VALUES (-100123456, ?, ?)`, m.role, m.content)
		if err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}

	return path
}

// createTargetDB creates an empty gobot-compatible SQLite database and returns its path.
func createTargetDB(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "gobot.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open target db: %v", err)
	}
	defer db.Close()
	// Just create the file; migrate.Run will create tables.
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS _placeholder (id INTEGER PRIMARY KEY)`)
	return path
}

func TestDryRun_DoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	srcPath := createSourceDB(t, dir)
	dstPath := createTargetDB(t, dir)

	// Record target DB mtime before dry-run.
	statBefore, err := os.Stat(dstPath)
	if err != nil {
		t.Fatalf("stat target before: %v", err)
	}

	opts := migrate.Options{
		SourceDB: srcPath,
		TargetDB: dstPath,
		AgentID:  "default",
		DryRun:   true,
	}

	report, err := migrate.Run(opts)
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}

	// Target DB must not be modified.
	statAfter, err := os.Stat(dstPath)
	if err != nil {
		t.Fatalf("stat target after: %v", err)
	}
	if statBefore.ModTime() != statAfter.ModTime() {
		t.Error("target DB was modified during dry-run")
	}
	if report.BackupPath != "" {
		t.Errorf("dry-run should not create backup, got %q", report.BackupPath)
	}
	if report.SessionsTotal != 2 {
		t.Errorf("want 2 sessions total, got %d", report.SessionsTotal)
	}
	if report.MessagesTotal != 2 {
		t.Errorf("want 2 messages total, got %d", report.MessagesTotal)
	}
}

func TestApplyMode_MigratesSessions(t *testing.T) {
	dir := t.TempDir()
	srcPath := createSourceDB(t, dir)
	dstPath := createTargetDB(t, dir)

	opts := migrate.Options{
		SourceDB:  srcPath,
		TargetDB:  dstPath,
		AgentID:   "myagent",
		DryRun:    false,
		BackupDir: filepath.Join(dir, "backups"),
	}

	report, err := migrate.Run(opts)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	if report.SessionsMigrated != 2 {
		t.Errorf("want 2 sessions migrated, got %d", report.SessionsMigrated)
	}
	if report.MessagesMigrated != 2 {
		t.Errorf("want 2 messages migrated, got %d", report.MessagesMigrated)
	}
	if len(report.Errors) > 0 {
		t.Errorf("unexpected errors: %v", report.Errors)
	}

	// Verify rows in target DB.
	db, err := sql.Open("sqlite3", dstPath)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer db.Close()

	var count int
	db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count)
	if count != 2 {
		t.Errorf("want 2 sessions in target DB, got %d", count)
	}

	db.QueryRow("SELECT COUNT(*) FROM session_messages").Scan(&count)
	if count != 2 {
		t.Errorf("want 2 messages in target DB, got %d", count)
	}
}

func TestApplyMode_CreatesBackup(t *testing.T) {
	dir := t.TempDir()
	srcPath := createSourceDB(t, dir)
	dstPath := createTargetDB(t, dir)
	backupDir := filepath.Join(dir, "backups")

	opts := migrate.Options{
		SourceDB:  srcPath,
		TargetDB:  dstPath,
		AgentID:   "default",
		BackupDir: backupDir,
	}

	report, err := migrate.Run(opts)
	if err != nil {
		t.Fatalf("apply failed: %v", err)
	}

	if report.BackupPath == "" {
		t.Fatal("expected backup path in report")
	}
	if _, err := os.Stat(report.BackupPath); os.IsNotExist(err) {
		t.Errorf("backup file not found at %q", report.BackupPath)
	}
}

func TestApplyMode_SkipsDuplicates(t *testing.T) {
	dir := t.TempDir()
	srcPath := createSourceDB(t, dir)
	dstPath := createTargetDB(t, dir)

	opts := migrate.Options{
		SourceDB:  srcPath,
		TargetDB:  dstPath,
		AgentID:   "default",
		BackupDir: filepath.Join(dir, "backups"),
	}

	// First run.
	r1, err := migrate.Run(opts)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if r1.SessionsMigrated != 2 {
		t.Errorf("first run: want 2 migrated, got %d", r1.SessionsMigrated)
	}

	// Second run: all sessions should be skipped.
	r2, err := migrate.Run(opts)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if r2.SessionsMigrated != 0 {
		t.Errorf("second run: want 0 migrated, got %d", r2.SessionsMigrated)
	}
	if r2.SessionsSkipped != 2 {
		t.Errorf("second run: want 2 skipped, got %d", r2.SessionsSkipped)
	}
	if r2.MessagesMigrated != 0 {
		t.Errorf("second run: want 0 messages migrated, got %d", r2.MessagesMigrated)
	}
	if r2.MessagesSkipped != 2 {
		t.Errorf("second run: want 2 messages skipped, got %d", r2.MessagesSkipped)
	}

	db, err := sql.Open("sqlite3", dstPath)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM session_messages").Scan(&count); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if count != 2 {
		t.Errorf("second run: want 2 messages in target DB, got %d", count)
	}
}

func TestKeyMapping_CanonicalKeys(t *testing.T) {
	dir := t.TempDir()
	srcPath := createSourceDB(t, dir)

	opts := migrate.Options{
		SourceDB: srcPath,
		TargetDB: filepath.Join(dir, "gobot.db"),
		AgentID:  "myagent",
		DryRun:   true,
	}

	report, err := migrate.Run(opts)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if len(report.KeyMapping) != 2 {
		t.Fatalf("want 2 key mappings, got %d", len(report.KeyMapping))
	}

	for _, km := range report.KeyMapping {
		switch km.ChatID {
		case -100123456:
			if km.ChatType != "group" {
				t.Errorf("chat_id=-100123456: want type=group, got %q", km.ChatType)
			}
			want := "agent:myagent:telegram:group:-100123456"
			if km.CanonicalKey != want {
				t.Errorf("want canonical key %q, got %q", want, km.CanonicalKey)
			}
		case 987654321:
			if km.ChatType != "private" {
				t.Errorf("chat_id=987654321: want type=private, got %q", km.ChatType)
			}
			want := "agent:myagent:telegram:dm:987654321"
			if km.CanonicalKey != want {
				t.Errorf("want canonical key %q, got %q", want, km.CanonicalKey)
			}
		default:
			t.Errorf("unexpected chat_id %d in key mapping", km.ChatID)
		}
	}
}

func TestSourceSchemaValidation_FailsWithoutSessionsTable(t *testing.T) {
	dir := t.TempDir()
	badDB := filepath.Join(dir, "bad.db")
	db, err := sql.Open("sqlite3", badDB)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`CREATE TABLE something_else (id INTEGER PRIMARY KEY)`)
	db.Close()

	opts := migrate.Options{
		SourceDB: badDB,
		TargetDB: filepath.Join(dir, "gobot.db"),
		DryRun:   true,
	}

	_, err = migrate.Run(opts)
	if err == nil {
		t.Fatal("expected error for DB without sessions table, got nil")
	}
}

func TestWorkspaceFileCopy(t *testing.T) {
	dir := t.TempDir()
	srcPath := createSourceDB(t, dir)

	// Create source workspace with a couple of files.
	srcWS := filepath.Join(dir, "openclaw-ws")
	_ = os.MkdirAll(filepath.Join(srcWS, "memory"), 0750)
	_ = os.WriteFile(filepath.Join(srcWS, "SOUL.md"), []byte("# Soul\n"), 0640)
	_ = os.WriteFile(filepath.Join(srcWS, "memory", "note.md"), []byte("# Note\n"), 0640)

	dstWS := filepath.Join(dir, "gobot-ws")

	opts := migrate.Options{
		SourceDB:        srcPath,
		TargetDB:        filepath.Join(dir, "gobot.db"),
		SourceWorkspace: srcWS,
		TargetWorkspace: dstWS,
		AgentID:         "default",
		BackupDir:       filepath.Join(dir, "backups"),
	}

	report, err := migrate.Run(opts)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}

	if report.WorkspaceFiles != 2 {
		t.Errorf("want 2 workspace files, got %d", report.WorkspaceFiles)
	}

	for _, rel := range []string{"SOUL.md", "memory/note.md"} {
		dst := filepath.Join(dstWS, rel)
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			t.Errorf("workspace file not copied: %s", dst)
		}
	}
}

func TestWorkspaceCopyRequiresTargetWorkspace(t *testing.T) {
	dir := t.TempDir()
	srcPath := createSourceDB(t, dir)

	opts := migrate.Options{
		SourceDB:        srcPath,
		TargetDB:        filepath.Join(dir, "gobot.db"),
		SourceWorkspace: filepath.Join(dir, "openclaw-ws"),
		DryRun:          true,
	}

	_, err := migrate.Run(opts)
	if err == nil {
		t.Fatal("expected error when source workspace is set without target workspace")
	}
}
