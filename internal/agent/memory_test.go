package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAppendQuickNoteToToday_CreatesNoteWithExpectedFormat(t *testing.T) {
	basePath := t.TempDir()
	m := &Memory{BasePath: basePath}

	const quickNote = "Remember to verify the deployment checklists."
	if err := m.AppendQuickNoteToToday(quickNote); err != nil {
		t.Fatalf("AppendQuickNoteToToday failed: %v", err)
	}

	today := time.Now().Format("2006-01-02")
	notePath := filepath.Join(basePath, "memory", today+".md")

	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("failed to read daily note: %v", err)
	}

	content := string(data)
	expectedHeader := fmt.Sprintf("# Memory: %s\n\n", today)
	if !strings.HasPrefix(content, expectedHeader) {
		t.Fatalf("expected note to start with header %q, got %q", expectedHeader, content)
	}

	re := regexp.MustCompile(`\n\n## Quick Note \(\d{2}:\d{2}\)\nRemember to verify the deployment checklists\.$`)
	if !re.MatchString(content) {
		t.Fatalf("daily note does not contain expected quick note format; content: %q", content)
	}
}

func TestAppendQuickNoteToToday_AppendsToExistingNote(t *testing.T) {
	basePath := t.TempDir()
	m := &Memory{BasePath: basePath}

	today := time.Now().Format("2006-01-02")
	memoryDir := filepath.Join(basePath, "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("failed to create memory dir: %v", err)
	}

	notePath := filepath.Join(memoryDir, today+".md")
	existing := fmt.Sprintf("# Memory: %s\n\n## 08:15\nUser: Existing entry\n", today)
	if err := os.WriteFile(notePath, []byte(existing), 0o644); err != nil {
		t.Fatalf("failed to seed note: %v", err)
	}

	if err := m.AppendQuickNoteToToday("Ship /note without an LLM turn."); err != nil {
		t.Fatalf("AppendQuickNoteToToday failed: %v", err)
	}

	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("failed to read updated note: %v", err)
	}

	content := string(data)
	if !strings.HasPrefix(content, existing) {
		t.Fatalf("existing content was not preserved; got: %q", content)
	}
	if !strings.Contains(content, "\n\n## Quick Note (") {
		t.Fatalf("quick note heading not found; got: %q", content)
	}
	if !strings.HasSuffix(content, "Ship /note without an LLM turn.") {
		t.Fatalf("quick note text should be the last line; got: %q", content)
	}
}
