package storage

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// newV2TestStore creates a temporary SQLite store for testing.
func newV2TestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestV2TablesCreated verifies that the v2 tables are created by the migration.
func TestV2TablesCreated(t *testing.T) {
	s := newV2TestStore(t)

	tables := []string{"sessions_v2", "session_messages_v2", "session_summary_nodes", "session_routes"}
	for _, tbl := range tables {
		var name string
		err := s.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", tbl, err)
		}
	}
}

// TestUpsertAndGetSessionV2 verifies basic insert/read of sessions_v2.
func TestUpsertAndGetSessionV2(t *testing.T) {
	s := newV2TestStore(t)

	sess := &SessionV2{
		SessionKey:      "agent:bot:telegram:group:-100",
		AgentID:         "bot",
		ModelOverride:   "claude-3-5-sonnet",
		ThinkLevel:      "low",
		ActiveAgent:     "default",
		UsageMode:       "on",
		Verbose:         true,
		QueueMode:       "collect",
		QueueDebounceMs: 2000,
	}
	if err := s.UpsertSessionV2(sess); err != nil {
		t.Fatalf("UpsertSessionV2: %v", err)
	}

	got, err := s.GetSessionV2(sess.SessionKey)
	if err != nil {
		t.Fatalf("GetSessionV2: %v", err)
	}
	if got == nil {
		t.Fatal("expected session, got nil")
	}
	if got.SessionKey != sess.SessionKey {
		t.Errorf("SessionKey = %q, want %q", got.SessionKey, sess.SessionKey)
	}
	if got.ModelOverride != sess.ModelOverride {
		t.Errorf("ModelOverride = %q, want %q", got.ModelOverride, sess.ModelOverride)
	}
	if !got.Verbose {
		t.Error("Verbose should be true")
	}
	if got.QueueDebounceMs != 2000 {
		t.Errorf("QueueDebounceMs = %d, want 2000", got.QueueDebounceMs)
	}
}

// TestGetSessionV2NotFound verifies that missing keys return nil without error.
func TestGetSessionV2NotFound(t *testing.T) {
	s := newV2TestStore(t)

	got, err := s.GetSessionV2("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

// TestSaveAndGetSessionMessagesV2 verifies message storage and retrieval.
func TestSaveAndGetSessionMessagesV2(t *testing.T) {
	s := newV2TestStore(t)
	key := "agent:bot:main"

	// Seed a v2 session so message_count updates work.
	if err := s.UpsertSessionV2(&SessionV2{SessionKey: key, AgentID: "bot"}); err != nil {
		t.Fatalf("UpsertSessionV2: %v", err)
	}

	msgs := []struct{ role, content, runID string }{
		{"user", "hello", ""},
		{"assistant", "hi there", "run-1"},
		{"user", "bye", ""},
	}
	for _, m := range msgs {
		if err := s.SaveSessionMessageV2(key, m.role, m.content, m.runID); err != nil {
			t.Fatalf("SaveSessionMessageV2: %v", err)
		}
	}

	got, err := s.GetSessionMessagesV2(key, 10)
	if err != nil {
		t.Fatalf("GetSessionMessagesV2: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got))
	}
	if got[0].Role != "user" || got[0].Content != "hello" {
		t.Errorf("unexpected first message: %+v", got[0])
	}
	if got[1].RunID != "run-1" {
		t.Errorf("expected run_id 'run-1', got %q", got[1].RunID)
	}

	// Verify message_count was incremented.
	sess, err := s.GetSessionV2(key)
	if err != nil {
		t.Fatalf("GetSessionV2: %v", err)
	}
	if sess.MessageCount != 3 {
		t.Errorf("MessageCount = %d, want 3", sess.MessageCount)
	}
}

func TestSaveSessionMessagePairV2StoresRunID(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "pair.db"))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	stmts := []string{
		`CREATE TABLE sessions_v2 (
			session_key TEXT PRIMARY KEY,
			message_count INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE session_messages_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_key TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			run_id TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`INSERT INTO sessions_v2 (session_key, message_count) VALUES ('agent:bot:pair', 0);`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed schema: %v\nSQL: %s", err, stmt)
		}
	}

	s := &Store{db: db}
	key := "agent:bot:pair"
	if err := s.SaveSessionMessagePairV2(key, "hello", "world", "job-1"); err != nil {
		t.Fatalf("SaveSessionMessagePairV2: %v", err)
	}

	got, err := s.GetSessionMessagesV2(key, 10)
	if err != nil {
		t.Fatalf("GetSessionMessagesV2: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(got))
	}
	for i, msg := range got {
		if msg.RunID != "job-1" {
			t.Fatalf("message %d run_id = %q, want job-1", i, msg.RunID)
		}
	}

	var count int
	if err := db.QueryRow(`SELECT message_count FROM sessions_v2 WHERE session_key = ?`, key).Scan(&count); err != nil {
		t.Fatalf("query message_count: %v", err)
	}
	if count != 2 {
		t.Fatalf("message_count = %d, want 2", count)
	}
}

func TestGetSessionMessagesV2AfterID(t *testing.T) {
	s := newV2TestStore(t)
	key := "agent:bot:main"

	if err := s.UpsertSessionV2(&SessionV2{SessionKey: key, AgentID: "bot"}); err != nil {
		t.Fatalf("UpsertSessionV2: %v", err)
	}

	for i, msg := range []struct {
		role    string
		content string
	}{
		{role: "user", content: "one"},
		{role: "assistant", content: "two"},
		{role: "user", content: "three"},
		{role: "assistant", content: "four"},
	} {
		if err := s.SaveSessionMessageV2(key, msg.role, msg.content, ""); err != nil {
			t.Fatalf("SaveSessionMessageV2 #%d: %v", i, err)
		}
	}

	all, err := s.GetAllSessionMessagesV2(key)
	if err != nil {
		t.Fatalf("GetAllSessionMessagesV2: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("expected 4 messages, got %d", len(all))
	}

	got, err := s.GetSessionMessagesV2AfterID(key, all[1].ID, 10)
	if err != nil {
		t.Fatalf("GetSessionMessagesV2AfterID: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 messages after ID %d, got %d", all[1].ID, len(got))
	}
	if got[0].Content != "three" || got[1].Content != "four" {
		t.Fatalf("unexpected messages after cursor: %+v", got)
	}
}

func TestReplaceAndGetSessionSummaryNodes(t *testing.T) {
	s := newV2TestStore(t)
	key := "agent:bot:main"

	if err := s.UpsertSessionV2(&SessionV2{SessionKey: key, AgentID: "bot"}); err != nil {
		t.Fatalf("UpsertSessionV2: %v", err)
	}

	first := []SessionSummaryNode{
		{
			SessionKey:           key,
			NodeKey:              "d0:0000",
			Depth:                0,
			Ordinal:              0,
			Content:              "D0 summary",
			SourceStartMessageID: 1,
			SourceEndMessageID:   4,
		},
		{
			SessionKey:           key,
			NodeKey:              "d1:0000",
			Depth:                1,
			Ordinal:              0,
			Content:              "D1 summary",
			SourceStartMessageID: 1,
			SourceEndMessageID:   4,
			ChildStartKey:        "d0:0000",
			ChildEndKey:          "d0:0000",
		},
	}
	if err := s.ReplaceSessionSummaryNodes(key, first); err != nil {
		t.Fatalf("ReplaceSessionSummaryNodes(first): %v", err)
	}

	second := []SessionSummaryNode{
		{
			SessionKey:           key,
			NodeKey:              "d2:0000",
			Depth:                2,
			Ordinal:              0,
			Content:              "D2 summary",
			SourceStartMessageID: 1,
			SourceEndMessageID:   8,
			ChildStartKey:        "d1:0000",
			ChildEndKey:          "d1:0001",
		},
	}
	if err := s.ReplaceSessionSummaryNodes(key, second); err != nil {
		t.Fatalf("ReplaceSessionSummaryNodes(second): %v", err)
	}

	got, err := s.GetSessionSummaryNodes(key)
	if err != nil {
		t.Fatalf("GetSessionSummaryNodes: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 summary node after replacement, got %d", len(got))
	}
	if got[0].NodeKey != "d2:0000" || got[0].ChildStartKey != "d1:0000" || got[0].SourceEndMessageID != 8 {
		t.Fatalf("unexpected summary node: %+v", got[0])
	}
}

func TestResetSessionClearsSessionSummaryNodes(t *testing.T) {
	s := newV2TestStore(t)
	chatID := int64(42)

	if _, err := s.db.Exec(`INSERT INTO sessions (chat_id, active_agent) VALUES (?, ?)`, chatID, "bot"); err != nil {
		t.Fatalf("seed legacy session: %v", err)
	}

	canonicalKey := canonicalTelegramSessionKey(chatID, "bot")
	keys := []string{
		canonicalKey,
		fmt.Sprintf("dm:%d", chatID),
		fmt.Sprintf("group:%d", chatID),
	}
	if err := s.UpsertSessionV2(&SessionV2{SessionKey: canonicalKey, AgentID: "bot"}); err != nil {
		t.Fatalf("UpsertSessionV2: %v", err)
	}
	if err := s.SaveSessionMessageV2(canonicalKey, "user", "hello", ""); err != nil {
		t.Fatalf("SaveSessionMessageV2: %v", err)
	}

	for i, key := range keys {
		if err := s.ReplaceSessionSummaryNodes(key, []SessionSummaryNode{{
			SessionKey:           key,
			NodeKey:              fmt.Sprintf("node-%d", i),
			Depth:                1,
			Ordinal:              0,
			Content:              "summary",
			SourceStartMessageID: 1,
			SourceEndMessageID:   2,
		}}); err != nil {
			t.Fatalf("ReplaceSessionSummaryNodes(%q): %v", key, err)
		}
	}

	if err := s.ResetSession(chatID); err != nil {
		t.Fatalf("ResetSession: %v", err)
	}

	for _, key := range keys {
		nodes, err := s.GetSessionSummaryNodes(key)
		if err != nil {
			t.Fatalf("GetSessionSummaryNodes(%q): %v", key, err)
		}
		if len(nodes) != 0 {
			t.Fatalf("expected summary nodes for %q to be cleared, got %+v", key, nodes)
		}
	}

	msgs, err := s.GetSessionMessagesV2(canonicalKey, 10)
	if err != nil {
		t.Fatalf("GetSessionMessagesV2: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected canonical transcript to be cleared, got %+v", msgs)
	}
}

// TestUpsertAndGetSessionRoute verifies session_routes CRUD.
func TestUpsertAndGetSessionRoute(t *testing.T) {
	s := newV2TestStore(t)

	route := SessionRoute{
		SessionKey: "agent:bot:telegram:group:-100",
		Channel:    "telegram",
		ChatID:     -100,
		ThreadID:   5,
		UserID:     42,
		Username:   "alice",
	}
	if err := s.UpsertSessionRoute(route); err != nil {
		t.Fatalf("UpsertSessionRoute: %v", err)
	}

	got, err := s.GetSessionRoute(route.SessionKey)
	if err != nil {
		t.Fatalf("GetSessionRoute: %v", err)
	}
	if got == nil {
		t.Fatal("expected route, got nil")
	}
	if got.ChatID != -100 {
		t.Errorf("ChatID = %d, want -100", got.ChatID)
	}
	if got.Username != "alice" {
		t.Errorf("Username = %q, want alice", got.Username)
	}
}

// TestGetSessionRouteNotFound verifies missing keys return nil without error.
func TestGetSessionRouteNotFound(t *testing.T) {
	s := newV2TestStore(t)

	got, err := s.GetSessionRoute("nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

// TestPromoteLegacySession_NoLegacy verifies that promotion creates a bare v2
// session even when there is no legacy data for chatID.
func TestPromoteLegacySession_NoLegacy(t *testing.T) {
	s := newV2TestStore(t)

	key := "agent:bot:telegram:group:-200"
	if err := s.PromoteLegacySession(key, "bot", "telegram", -200); err != nil {
		t.Fatalf("PromoteLegacySession: %v", err)
	}

	sess, err := s.GetSessionV2(key)
	if err != nil {
		t.Fatalf("GetSessionV2: %v", err)
	}
	if sess == nil {
		t.Fatal("expected v2 session after promotion")
	}
	if sess.PromotedFromChatID != 0 {
		t.Errorf("PromotedFromChatID = %d, want 0 (no legacy)", sess.PromotedFromChatID)
	}

	route, err := s.GetSessionRoute(key)
	if err != nil {
		t.Fatalf("GetSessionRoute: %v", err)
	}
	if route == nil || route.ChatID != -200 {
		t.Errorf("unexpected route: %+v", route)
	}
}

// TestPromoteLegacySession_WithLegacy verifies that promotion copies metadata
// and messages from the legacy tables, leaving the originals intact.
func TestPromoteLegacySession_WithLegacy(t *testing.T) {
	s := newV2TestStore(t)

	chatID := int64(-300)

	// Populate legacy session.
	if err := s.SaveSession(chatID, ""); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := s.SetModelOverride(chatID, "gpt-4o"); err != nil {
		t.Fatalf("SetModelOverride: %v", err)
	}

	// Populate legacy messages.
	for _, pair := range [][2]string{
		{"user", "legacy message 1"},
		{"assistant", "legacy reply 1"},
	} {
		if err := s.SaveSessionMessage(chatID, pair[0], pair[1]); err != nil {
			t.Fatalf("SaveSessionMessage: %v", err)
		}
	}

	key := "agent:bot:telegram:group:-300"
	if err := s.PromoteLegacySession(key, "bot", "telegram", chatID); err != nil {
		t.Fatalf("PromoteLegacySession: %v", err)
	}

	// Check v2 session has legacy metadata.
	sess, err := s.GetSessionV2(key)
	if err != nil {
		t.Fatalf("GetSessionV2: %v", err)
	}
	if sess == nil {
		t.Fatal("expected v2 session")
	}
	if sess.ModelOverride != "gpt-4o" {
		t.Errorf("ModelOverride = %q, want gpt-4o", sess.ModelOverride)
	}
	if sess.PromotedFromChatID != chatID {
		t.Errorf("PromotedFromChatID = %d, want %d", sess.PromotedFromChatID, chatID)
	}

	// Check v2 messages were copied.
	msgs, err := s.GetSessionMessagesV2(key, 10)
	if err != nil {
		t.Fatalf("GetSessionMessagesV2: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 v2 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "legacy message 1" {
		t.Errorf("unexpected first message: %q", msgs[0].Content)
	}

	// Legacy tables must be untouched.
	legacy, err := s.GetSessionMessages(chatID, 10)
	if err != nil {
		t.Fatalf("GetSessionMessages (legacy): %v", err)
	}
	if len(legacy) != 2 {
		t.Fatalf("legacy messages should still be 2, got %d", len(legacy))
	}
}

// TestPromoteLegacySession_Idempotent verifies that a second call is a no-op.
func TestPromoteLegacySession_Idempotent(t *testing.T) {
	s := newV2TestStore(t)

	key := "agent:bot:telegram:group:-400"
	chatID := int64(-400)

	for i := 0; i < 3; i++ {
		if err := s.PromoteLegacySession(key, "bot", "telegram", chatID); err != nil {
			t.Fatalf("call %d: PromoteLegacySession: %v", i, err)
		}
	}

	// Should only have one sessions_v2 row.
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM sessions_v2 WHERE session_key = ?", key).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 sessions_v2 row, got %d", count)
	}
}
