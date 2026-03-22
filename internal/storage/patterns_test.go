package storage

import (
	"fmt"
	"path/filepath"
	"testing"
)

func newPatternsTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestGetChatActivityStats_Empty(t *testing.T) {
	s := newPatternsTestStore(t)
	rows, err := s.GetChatActivityStats(10)
	if err != nil {
		t.Fatalf("GetChatActivityStats: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestGetChatActivityStats_WithData(t *testing.T) {
	s := newPatternsTestStore(t)

	// Insert a v2 session with messages.
	sess := &SessionV2{
		SessionKey:   "agent:bot:telegram:group:-100",
		AgentID:      "bot",
		MessageCount: 42,
	}
	if err := s.UpsertSessionV2(sess); err != nil {
		t.Fatalf("UpsertSessionV2: %v", err)
	}

	// Insert a route so chat_id is populated.
	if err := s.UpsertSessionRoute(SessionRoute{
		SessionKey: sess.SessionKey,
		Channel:    "telegram",
		ChatID:     -100,
	}); err != nil {
		t.Fatalf("UpsertSessionRoute: %v", err)
	}

	rows, err := s.GetChatActivityStats(10)
	if err != nil {
		t.Fatalf("GetChatActivityStats: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].ChatID != -100 {
		t.Errorf("ChatID = %d, want -100", rows[0].ChatID)
	}
	if rows[0].MessageCount != 42 {
		t.Errorf("MessageCount = %d, want 42", rows[0].MessageCount)
	}
}

func TestGetRecentUserMessages_Empty(t *testing.T) {
	s := newPatternsTestStore(t)
	msgs, err := s.GetRecentUserMessages(10)
	if err != nil {
		t.Fatalf("GetRecentUserMessages: %v", err)
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

func TestGetRecentUserMessages_WithData(t *testing.T) {
	s := newPatternsTestStore(t)

	sess := &SessionV2{
		SessionKey: "agent:bot:telegram:group:-200",
		AgentID:    "bot",
	}
	if err := s.UpsertSessionV2(sess); err != nil {
		t.Fatalf("UpsertSessionV2: %v", err)
	}

	if err := s.SaveSessionMessageV2(sess.SessionKey, "user", "check the status", ""); err != nil {
		t.Fatalf("SaveSessionMessageV2: %v", err)
	}
	if err := s.SaveSessionMessageV2(sess.SessionKey, "assistant", "all good", ""); err != nil {
		t.Fatalf("SaveSessionMessageV2: %v", err)
	}

	msgs, err := s.GetRecentUserMessages(10)
	if err != nil {
		t.Fatalf("GetRecentUserMessages: %v", err)
	}
	// Only user messages should be returned.
	if len(msgs) != 1 {
		t.Fatalf("expected 1 user message, got %d", len(msgs))
	}
	if msgs[0].Content != "check the status" {
		t.Errorf("Content = %q, want %q", msgs[0].Content, "check the status")
	}
}

func TestJobKindCounts_Empty(t *testing.T) {
	s := newPatternsTestStore(t)
	counts, err := s.JobKindCounts(10)
	if err != nil {
		t.Fatalf("JobKindCounts: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("expected empty counts, got %v", counts)
	}
}

func TestJobKindCounts_WithData(t *testing.T) {
	s := newPatternsTestStore(t)

	for i := 0; i < 3; i++ {
		if err := s.CreateJob(Job{
			JobID:  fmt.Sprintf("job-%d", i),
			Kind:   "backup",
			Status: "succeeded",
		}); err != nil {
			t.Fatalf("CreateJob: %v", err)
		}
	}

	counts, err := s.JobKindCounts(10)
	if err != nil {
		t.Fatalf("JobKindCounts: %v", err)
	}
	if counts["backup"] != 3 {
		t.Errorf("backup count = %d, want 3", counts["backup"])
	}
}
