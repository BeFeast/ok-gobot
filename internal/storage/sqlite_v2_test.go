package storage

import (
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

	tables := []string{"sessions_v2", "session_messages_v2", "session_routes"}
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
