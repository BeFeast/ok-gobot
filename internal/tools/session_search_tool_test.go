package tools

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestSessionGetToolUnconfiguredReturnsError(t *testing.T) {
	tool := NewSessionGetTool(nil)
	if _, err := tool.Execute(context.Background(), "agent:default:telegram:dm:1"); err == nil {
		t.Fatal("expected error when source is not configured")
	}
}

func TestSessionSearchToolUnconfiguredReturnsError(t *testing.T) {
	tool := NewSessionSearchTool(nil, nil, nil)
	if _, err := tool.Execute(context.Background(), "anything"); err == nil {
		t.Fatal("expected error when session memory is not configured")
	}
}

func TestSessionGetToolFormatsSpan(t *testing.T) {
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	if _, err := db.Exec(`
		CREATE TABLE session_messages_v2 (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_key TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			run_id TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	const sessionKey = "agent:default:telegram:dm:7"
	for _, msg := range []struct{ role, content string }{
		{"user", "hello there"},
		{"assistant", "hi! how can I help?"},
		{"user", "remember sk-abcdefghij1234567890 token"},
		{"assistant", "noted privately"},
	} {
		if _, err := db.Exec(
			`INSERT INTO session_messages_v2 (session_key, role, content) VALUES (?, ?, ?)`,
			sessionKey, msg.role, msg.content,
		); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	tool := NewSessionGetTool(db)
	out, err := tool.Execute(context.Background(), sessionKey, "3", "1")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "fingerprint=") {
		t.Errorf("missing fingerprint header: %q", out)
	}
	if strings.Contains(out, "sk-abcdefghij1234567890") {
		t.Errorf("session_get leaked secret: %q", out)
	}
	if !strings.Contains(out, "> msg 3") {
		t.Errorf("anchor marker missing: %q", out)
	}
}
