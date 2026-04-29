package tools

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestSessionGetToolExecuteJSONReadsByName ensures named arguments are
// honored regardless of map iteration order, so message_id and span are
// never swapped.
func TestSessionGetToolExecuteJSONReadsByName(t *testing.T) {
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

	const sessionKey = "agent:default:telegram:dm:9"
	for _, msg := range []struct{ role, content string }{
		{"user", "first"},
		{"assistant", "second"},
		{"user", "third"},
		{"assistant", "fourth"},
		{"user", "fifth"},
	} {
		if _, err := db.Exec(
			`INSERT INTO session_messages_v2 (session_key, role, content) VALUES (?, ?, ?)`,
			sessionKey, msg.role, msg.content,
		); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	tool := NewSessionGetTool(db)
	out, err := tool.ExecuteJSON(context.Background(), map[string]string{
		"session_key": sessionKey,
		"message_id":  "3",
		"span":        "1",
	})
	if err != nil {
		t.Fatalf("ExecuteJSON: %v", err)
	}
	if !strings.Contains(out, "Anchor: msg 3") {
		t.Errorf("anchor not honoured by name: %q", out)
	}
	if !strings.Contains(out, "> msg 3") {
		t.Errorf("anchor marker missing: %q", out)
	}
	// span=1 around msg 3 → 3 messages (2,3,4); never 5.
	if strings.Contains(out, "msg 5") {
		t.Errorf("span=1 leaked far-away message: %q", out)
	}
}

// TestSessionGetToolClipsOnRuneBoundary ensures multi-byte content does
// not get sliced mid-codepoint, which previously produced invalid UTF-8.
func TestSessionGetToolClipsOnRuneBoundary(t *testing.T) {
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

	const sessionKey = "agent:default:telegram:dm:11"
	// Each "🚀" is a 4-byte UTF-8 sequence; 250 of them = 1000 bytes,
	// which is over the 800-byte session_get clip threshold.
	emojiBlob := strings.Repeat("🚀", 250)
	if _, err := db.Exec(
		`INSERT INTO session_messages_v2 (session_key, role, content) VALUES (?, ?, ?)`,
		sessionKey, "user", emojiBlob,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	tool := NewSessionGetTool(db)
	out, err := tool.Execute(context.Background(), sessionKey)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("session_get produced invalid UTF-8: %q", out)
	}
}
