package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Memory manages daily notes and long-term memory
type Memory struct {
	BasePath string
}

// NewMemory creates a new memory manager
func NewMemory(basePath string) *Memory {
	if basePath == "" {
		homeDir, _ := os.UserHomeDir()
		basePath = filepath.Join(homeDir, "clawd")
	}
	return &Memory{BasePath: basePath}
}

// DailyNote represents a single day's memory
type DailyNote struct {
	Date    string
	Content string
	Path    string
}

// GetTodayNote returns today's daily note (creates if doesn't exist)
func (m *Memory) GetTodayNote() (*DailyNote, error) {
	date := time.Now().Format("2006-01-02")
	return m.GetNote(date)
}

// GetNote returns a specific day's note
func (m *Memory) GetNote(date string) (*DailyNote, error) {
	memoryDir := filepath.Join(m.BasePath, "memory")
	if err := os.MkdirAll(memoryDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create memory directory: %w", err)
	}

	path := filepath.Join(memoryDir, date+".md")
	content := ""

	// Read if exists
	if data, err := os.ReadFile(path); err == nil {
		content = string(data)
	}

	return &DailyNote{
		Date:    date,
		Content: content,
		Path:    path,
	}, nil
}

// AppendToToday appends content to today's note
func (m *Memory) AppendToToday(content string) error {
	note, err := m.GetTodayNote()
	if err != nil {
		return err
	}

	timestamp := time.Now().Format("15:04")
	entry := fmt.Sprintf("\n## %s\n\n%s\n", timestamp, content)

	// Append to file
	f, err := os.OpenFile(note.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open note: %w", err)
	}
	defer f.Close()

	// Add header if new file
	if note.Content == "" {
		header := fmt.Sprintf("# Memory: %s\n\n", note.Date)
		if _, err := f.WriteString(header); err != nil {
			return err
		}
	}

	_, err = f.WriteString(entry)
	return err
}

// LoadLongTermMemory reads MEMORY.md
func (m *Memory) LoadLongTermMemory() (string, error) {
	path := filepath.Join(m.BasePath, "MEMORY.md")
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(content), nil
}

// UpdateLongTermMemory updates MEMORY.md with new content
func (m *Memory) UpdateLongTermMemory(content string) error {
	path := filepath.Join(m.BasePath, "MEMORY.md")
	return os.WriteFile(path, []byte(content), 0644)
}

// GetRecentContext loads the last N days of memory for context
func (m *Memory) GetRecentContext(days int) (string, error) {
	var context strings.Builder

	for i := 0; i < days; i++ {
		date := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
		note, err := m.GetNote(date)
		if err != nil {
			continue
		}
		if note.Content != "" {
			context.WriteString(fmt.Sprintf("\n--- %s ---\n%s\n", date, note.Content))
		}
	}

	return context.String(), nil
}

// IsMainSession returns true if this is a direct chat with the user
// (not a group chat or shared context)
func (m *Memory) IsMainSession(chatType string) bool {
	return chatType == "private" || chatType == "direct"
}

// ShouldLoadLongTermMemory determines if long-term memory should be loaded
func (m *Memory) ShouldLoadLongTermMemory(isMainSession bool) bool {
	// Only load MEMORY.md in main sessions for security
	// AGENTS.md: "DO NOT load in shared contexts (Discord, group chats, sessions with other people)"
	return isMainSession
}
