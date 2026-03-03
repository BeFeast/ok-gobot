package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultPath is the canonical default bootstrap directory.
	DefaultPath = "~/ok-gobot-soul"

	maxFileChars = 8000
)

var managedFiles = []string{
	"SOUL.md",
	"IDENTITY.md",
	"USER.md",
	"AGENTS.md",
	"TOOLS.md",
	"MEMORY.md",
	"HEARTBEAT.md",
}

// SkillEntry represents a discovered skill.
type SkillEntry struct {
	Name        string
	Description string
	Path        string
}

// Loader loads and exposes bootstrap context files.
type Loader struct {
	BasePath string
	Files    map[string]string
	Skills   []SkillEntry
	now      func() time.Time
}

// NewLoader creates a new bootstrap loader rooted at basePath.
func NewLoader(basePath string) (*Loader, error) {
	return newLoader(basePath, time.Now)
}

func newLoader(basePath string, now func() time.Time) (*Loader, error) {
	if now == nil {
		now = time.Now
	}
	if basePath == "" {
		basePath = DefaultPath
	}

	l := &Loader{
		BasePath: ExpandPath(basePath),
		Files:    make(map[string]string),
		now:      now,
	}

	if err := l.loadFiles(); err != nil {
		return nil, err
	}

	return l, nil
}

// ExpandPath expands a leading "~/" using the current user's home directory.
func ExpandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(homeDir, path[2:])
	}
	return path
}

// ManagedFiles returns the canonical bootstrap files.
func ManagedFiles() []string {
	files := make([]string, len(managedFiles))
	copy(files, managedFiles)
	return files
}

// Reload refreshes the bootstrap context from disk.
func (l *Loader) Reload() error {
	if l == nil {
		return nil
	}
	l.Files = make(map[string]string)
	l.Skills = nil
	return l.loadFiles()
}

func (l *Loader) loadFiles() error {
	for _, filename := range managedFiles {
		path := filepath.Join(l.BasePath, filename)
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("failed to read %s: %w", filename, err)
		}
		l.Files[filename] = truncateWithPreservation(string(content), maxFileChars)
	}

	now := l.currentTime()
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	for _, date := range []string{today, yesterday} {
		path := filepath.Join(l.BasePath, "memory", date+".md")
		content, err := os.ReadFile(path)
		if err == nil {
			l.Files["memory/"+date+".md"] = truncateWithPreservation(string(content), maxFileChars)
		}
	}

	if err := l.discoverSkills(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to discover skills: %v\n", err)
	}

	return nil
}

// SystemPrompt builds the markdown bootstrap prompt.
func (l *Loader) SystemPrompt() string {
	if l == nil {
		return ""
	}

	var prompt strings.Builder

	if identity, ok := l.Files["IDENTITY.md"]; ok {
		prompt.WriteString("## IDENTITY\n\n")
		prompt.WriteString(identity)
		prompt.WriteString("\n\n")
	}

	if soul, ok := l.Files["SOUL.md"]; ok {
		prompt.WriteString("## SOUL\n\n")
		prompt.WriteString(soul)
		prompt.WriteString("\n\n")
	}

	if user, ok := l.Files["USER.md"]; ok {
		prompt.WriteString("## USER CONTEXT\n\n")
		prompt.WriteString(user)
		prompt.WriteString("\n\n")
	}

	if agents, ok := l.Files["AGENTS.md"]; ok {
		prompt.WriteString("## AGENT PROTOCOL\n\n")
		prompt.WriteString(agents)
		prompt.WriteString("\n\n")
	}

	if toolsRef, ok := l.Files["TOOLS.md"]; ok {
		prompt.WriteString("## TOOLS REFERENCE\n\n")
		prompt.WriteString(toolsRef)
		prompt.WriteString("\n\n")
	}

	if memory, ok := l.Files["MEMORY.md"]; ok {
		prompt.WriteString("## LONG-TERM MEMORY\n\n")
		prompt.WriteString(memory)
		prompt.WriteString("\n\n")
	}

	today := l.currentTime().Format("2006-01-02")
	if daily, ok := l.Files["memory/"+today+".md"]; ok {
		prompt.WriteString("## TODAY'S ACTIVITY\n\n")
		prompt.WriteString(daily)
		prompt.WriteString("\n\n")
	}

	return prompt.String()
}

// MinimalPrompt builds the minimal IDENTITY+SOUL bootstrap prompt.
func (l *Loader) MinimalPrompt() string {
	if l == nil {
		return ""
	}

	var prompt strings.Builder

	if identity, ok := l.Files["IDENTITY.md"]; ok {
		prompt.WriteString("## IDENTITY\n\n")
		prompt.WriteString(identity)
		prompt.WriteString("\n\n")
	}

	if soul, ok := l.Files["SOUL.md"]; ok {
		prompt.WriteString("## SOUL\n\n")
		prompt.WriteString(soul)
		prompt.WriteString("\n\n")
	}

	return prompt.String()
}

// IdentityLine returns the ultra-minimal identity line.
func (l *Loader) IdentityLine() string {
	return fmt.Sprintf("You are %s %s.", l.Name(), l.Emoji())
}

// Name extracts the configured bootstrap name.
func (l *Loader) Name() string {
	if l == nil {
		return "Штрудель"
	}

	if identity, ok := l.Files["IDENTITY.md"]; ok {
		lines := strings.Split(identity, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "Name:") {
				parts := strings.SplitN(line, "Name:", 2)
				if len(parts) == 2 {
					name := strings.TrimSpace(parts[1])
					name = strings.Trim(name, "* ")
					return name
				}
			}
		}
	}

	return "Штрудель"
}

// Emoji extracts the configured bootstrap emoji.
func (l *Loader) Emoji() string {
	if l == nil {
		return "🕯️"
	}

	if identity, ok := l.Files["IDENTITY.md"]; ok {
		lines := strings.Split(identity, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "Emoji:") {
				parts := strings.SplitN(line, "Emoji:", 2)
				if len(parts) == 2 {
					return strings.TrimSpace(parts[1])
				}
			}
		}
	}

	return "🕯️"
}

// SkillsSummary returns a formatted list of available skills.
func (l *Loader) SkillsSummary() string {
	if l == nil || len(l.Skills) == 0 {
		return ""
	}

	var summary strings.Builder
	for _, skill := range l.Skills {
		dir := filepath.Dir(skill.Path)
		summary.WriteString(fmt.Sprintf("- %s (SKILL.md: %s, baseDir: %s): %s\n", skill.Name, skill.Path, dir, skill.Description))
	}

	return summary.String()
}

// FileContent returns the raw content of a loaded file.
func (l *Loader) FileContent(filename string) (string, bool) {
	if l == nil {
		return "", false
	}
	content, ok := l.Files[filename]
	return content, ok
}

// HasFile reports whether filename is present in the loaded bootstrap set.
func (l *Loader) HasFile(filename string) bool {
	if l == nil {
		return false
	}
	_, ok := l.Files[filename]
	return ok
}

func (l *Loader) discoverSkills() error {
	skillsPath := filepath.Join(l.BasePath, "skills")
	if _, err := os.Stat(skillsPath); os.IsNotExist(err) {
		return nil
	}

	entries, err := os.ReadDir(skillsPath)
	if err != nil {
		return fmt.Errorf("failed to read skills directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillName := entry.Name()
		skillFilePath := filepath.Join(skillsPath, skillName, "SKILL.md")
		content, err := os.ReadFile(skillFilePath)
		if err != nil {
			continue
		}

		description := ""
		lines := strings.Split(string(content), "\n")
		inFrontmatter := false
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "---" {
				inFrontmatter = !inFrontmatter
				continue
			}
			if inFrontmatter {
				if strings.HasPrefix(trimmed, "description:") {
					description = strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
				}
				continue
			}
			if trimmed != "" && !strings.HasPrefix(trimmed, "#") && description == "" {
				description = trimmed
				break
			}
		}

		if description == "" {
			description = "No description available"
		}

		l.Skills = append(l.Skills, SkillEntry{
			Name:        skillName,
			Description: description,
			Path:        skillFilePath,
		})
	}

	return nil
}

func truncateWithPreservation(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	half := maxChars / 2
	head := text[:half]
	tail := text[len(text)-half:]
	return head + "\n\n... [truncated " + strconv.Itoa(len(text)-maxChars) + " chars] ...\n\n" + tail
}

func (l *Loader) currentTime() time.Time {
	if l == nil || l.now == nil {
		return time.Now()
	}
	return l.now()
}
