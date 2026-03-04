package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestCanonicalSchemaTablesCreated(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	for _, table := range []string{"sessions_v2", "session_routes", "session_messages_v2", "run_queue_state", "subagent_runs"} {
		if !tableExists(t, store.DB(), table) {
			t.Fatalf("expected table %q to exist", table)
		}
	}
}

func TestCanonicalBackfillFromLegacyTables(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "legacy.db")
	seedLegacyDB(t, dbPath)

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	defer store.Close() //nolint:errcheck

	var (
		agentID      string
		state        string
		model        string
		usageMode    string
		verbose      int
		inputTokens  int
		outputTokens int
	)
	err = store.DB().QueryRow(`
		SELECT agent_id, state, model_override, usage_mode, verbose, input_tokens, output_tokens
		FROM sessions_v2
		WHERE session_key = ?
	`, "agent:chef:telegram:group:42").Scan(&agentID, &state, &model, &usageMode, &verbose, &inputTokens, &outputTokens)
	if err != nil {
		t.Fatalf("failed to query sessions_v2 backfill row: %v", err)
	}

	if agentID != "chef" {
		t.Fatalf("agent_id mismatch: got %q want %q", agentID, "chef")
	}
	if state != "legacy state" {
		t.Fatalf("state mismatch: got %q", state)
	}
	if model != "openai/gpt-4o-mini" {
		t.Fatalf("model_override mismatch: got %q", model)
	}
	if usageMode != "tokens" {
		t.Fatalf("usage_mode mismatch: got %q", usageMode)
	}
	if verbose != 1 {
		t.Fatalf("verbose mismatch: got %d", verbose)
	}
	if inputTokens != 21 || outputTokens != 34 {
		t.Fatalf("token mismatch: input=%d output=%d", inputTokens, outputTokens)
	}

	var msgCount int
	err = store.DB().QueryRow(`
		SELECT COUNT(*)
		FROM session_messages_v2
		WHERE session_key = ?
	`, "agent:chef:telegram:group:42").Scan(&msgCount)
	if err != nil {
		t.Fatalf("failed to query session_messages_v2 count: %v", err)
	}
	if msgCount != 1 {
		t.Fatalf("message backfill mismatch: got %d want 1", msgCount)
	}
}

func TestSessionRouteCRUD(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	route := SessionRoute{
		SessionKey:       "agent:default:telegram:group:100",
		Channel:          "telegram",
		ChatID:           100,
		ThreadID:         7,
		ReplyToMessageID: 88,
		UserID:           111,
		Username:         "alice",
	}
	if err := store.SaveSessionRoute(route); err != nil {
		t.Fatalf("SaveSessionRoute failed: %v", err)
	}

	got, err := store.GetSessionRoute(route.SessionKey)
	if err != nil {
		t.Fatalf("GetSessionRoute failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected route, got nil")
	}
	if got.ChatID != route.ChatID || got.ThreadID != route.ThreadID || got.UserID != route.UserID || got.Username != route.Username {
		t.Fatalf("route mismatch: got %+v", *got)
	}

	if err := store.DeleteSessionRoute(route.SessionKey); err != nil {
		t.Fatalf("DeleteSessionRoute failed: %v", err)
	}
	got, err = store.GetSessionRoute(route.SessionKey)
	if err != nil {
		t.Fatalf("GetSessionRoute after delete failed: %v", err)
	}
	if got != nil {
		t.Fatalf("expected route to be deleted, got %+v", *got)
	}
}

func TestRecordSubagentSpawnSetsCanonicalColumns(t *testing.T) {
	t.Parallel()

	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	err := store.RecordSubagentSpawn(
		"run-abc",
		"agent:chef:subagent:run-abc",
		"agent:chef:main",
		"chef",
		"summarize logs",
		"openai/gpt-4.1",
		"low",
		"file,grep",
		"/tmp/work",
		true,
	)
	if err != nil {
		t.Fatalf("RecordSubagentSpawn failed: %v", err)
	}

	var runID, childKey string
	err = store.DB().QueryRow(`
		SELECT run_id, child_session_key
		FROM subagent_runs
		WHERE run_slug = ?
	`, "run-abc").Scan(&runID, &childKey)
	if err != nil {
		t.Fatalf("failed to query subagent run: %v", err)
	}
	if runID != "run-abc" || childKey != "agent:chef:subagent:run-abc" {
		t.Fatalf("subagent canonical columns mismatch: run_id=%q child_session_key=%q", runID, childKey)
	}

	var sessions int
	err = store.DB().QueryRow(`
		SELECT COUNT(*)
		FROM sessions_v2
		WHERE session_key IN (?, ?)
	`, "agent:chef:main", "agent:chef:subagent:run-abc").Scan(&sessions)
	if err != nil {
		t.Fatalf("failed to query sessions_v2 for subagent keys: %v", err)
	}
	if sessions != 2 {
		t.Fatalf("expected parent+child sessions in sessions_v2, got %d", sessions)
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()

	store, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("New() failed: %v", err)
	}
	return store
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()

	var count int
	err := db.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name = ?
	`, table).Scan(&count)
	if err != nil {
		t.Fatalf("tableExists(%s) query failed: %v", table, err)
	}
	return count > 0
}

func seedLegacyDB(t *testing.T, dbPath string) {
	t.Helper()

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("sql.Open failed: %v", err)
	}
	defer db.Close() //nolint:errcheck

	stmts := []string{
		`CREATE TABLE sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id INTEGER UNIQUE NOT NULL,
			state TEXT,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			message_count INTEGER DEFAULT 0,
			last_summary TEXT,
			compaction_count INTEGER DEFAULT 0,
			model_override TEXT DEFAULT '',
			group_mode TEXT DEFAULT '',
			active_agent TEXT DEFAULT 'default',
			input_tokens INTEGER DEFAULT 0,
			output_tokens INTEGER DEFAULT 0,
			total_tokens INTEGER DEFAULT 0,
			context_tokens INTEGER DEFAULT 0,
			usage_mode TEXT DEFAULT 'off',
			think_level TEXT DEFAULT '',
			verbose INTEGER DEFAULT 0,
			queue_mode TEXT DEFAULT 'collect',
			queue_debounce_ms INTEGER DEFAULT 1500
		);`,
		`CREATE TABLE session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id INTEGER NOT NULL,
			chat_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`,
		`INSERT INTO sessions (
			chat_id,
			state,
			message_count,
			last_summary,
			compaction_count,
			model_override,
			active_agent,
			input_tokens,
			output_tokens,
			total_tokens,
			context_tokens,
			usage_mode,
			think_level,
			verbose,
			queue_mode,
			queue_debounce_ms
		) VALUES (
			42,
			'legacy state',
			1,
			'legacy summary',
			3,
			'openai/gpt-4o-mini',
			'chef',
			21,
			34,
			55,
			8192,
			'tokens',
			'low',
			1,
			'collect',
			1500
		);`,
		`INSERT INTO session_messages (session_id, chat_id, role, content) VALUES (1, 42, 'user', 'legacy question');`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed stmt failed: %v\nSQL: %s", err, stmt)
		}
	}
}
