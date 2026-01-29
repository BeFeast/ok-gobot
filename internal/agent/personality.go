// Package agent implements the AI agent personality and memory system
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const maxFileChars = 8000 // max characters per personality file

// truncateWithPreservation keeps head and tail of long text
func truncateWithPreservation(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	half := maxChars / 2
	head := text[:half]
	tail := text[len(text)-half:]
	return head + "\n\n... [truncated " + strconv.Itoa(len(text)-maxChars) + " chars] ...\n\n" + tail
}

// SkillEntry represents a discovered skill
type SkillEntry struct {
	Name        string // skill directory name
	Description string // first line of SKILL.md
	Path        string // full path to SKILL.md
}

// Personality loads and manages agent context files
type Personality struct {
	BasePath string
	Files    map[string]string // filename -> content
	Skills   []SkillEntry      // discovered skills
}

// NewPersonality creates a new personality loader
func NewPersonality(basePath string) (*Personality, error) {
	if basePath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		basePath = filepath.Join(homeDir, "ok-gobot-soul")
	}

	p := &Personality{
		BasePath: basePath,
		Files:    make(map[string]string),
	}

	// Load all context files
	if err := p.loadFiles(); err != nil {
		return nil, err
	}

	return p, nil
}

// loadFiles reads all markdown files from the soul directory
func (p *Personality) loadFiles() error {
	filesToLoad := []string{
		"SOUL.md",
		"IDENTITY.md",
		"USER.md",
		"AGENTS.md",
		"TOOLS.md",
		"MEMORY.md",
		"HEARTBEAT.md",
	}

	for _, filename := range filesToLoad {
		path := filepath.Join(p.BasePath, filename)
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue // Skip missing files
			}
			return fmt.Errorf("failed to read %s: %w", filename, err)
		}
		p.Files[filename] = truncateWithPreservation(string(content), maxFileChars)
	}

	// Load daily memory files
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	for _, date := range []string{today, yesterday} {
		path := filepath.Join(p.BasePath, "memory", date+".md")
		content, err := os.ReadFile(path)
		if err == nil {
			p.Files["memory/"+date+".md"] = truncateWithPreservation(string(content), maxFileChars)
		}
	}

	// Discover skills
	if err := p.discoverSkills(); err != nil {
		// Log error but don't fail personality loading
		fmt.Fprintf(os.Stderr, "Warning: failed to discover skills: %v\n", err)
	}

	return nil
}

// GetSystemPrompt builds the complete system prompt from all loaded files
func (p *Personality) GetSystemPrompt() string {
	var prompt strings.Builder

	// Start with IDENTITY
	if identity, ok := p.Files["IDENTITY.md"]; ok {
		prompt.WriteString("## IDENTITY\n\n")
		prompt.WriteString(identity)
		prompt.WriteString("\n\n")
	}

	// Add SOUL
	if soul, ok := p.Files["SOUL.md"]; ok {
		prompt.WriteString("## SOUL\n\n")
		prompt.WriteString(soul)
		prompt.WriteString("\n\n")
	}

	// Add USER context
	if user, ok := p.Files["USER.md"]; ok {
		prompt.WriteString("## USER CONTEXT\n\n")
		prompt.WriteString(user)
		prompt.WriteString("\n\n")
	}

	// Add AGENTS protocol
	if agents, ok := p.Files["AGENTS.md"]; ok {
		prompt.WriteString("## AGENT PROTOCOL\n\n")
		prompt.WriteString(agents)
		prompt.WriteString("\n\n")
	}

	// Add TOOLS reference
	if tools, ok := p.Files["TOOLS.md"]; ok {
		prompt.WriteString("## TOOLS REFERENCE\n\n")
		prompt.WriteString(tools)
		prompt.WriteString("\n\n")
	}

	// Add MEMORY (curated long-term memory)
	if memory, ok := p.Files["MEMORY.md"]; ok {
		prompt.WriteString("## LONG-TERM MEMORY\n\n")
		prompt.WriteString(memory)
		prompt.WriteString("\n\n")
	}

	// Add recent daily memory
	today := time.Now().Format("2006-01-02")
	if daily, ok := p.Files["memory/"+today+".md"]; ok {
		prompt.WriteString("## TODAY'S ACTIVITY\n\n")
		prompt.WriteString(daily)
		prompt.WriteString("\n\n")
	}

	return prompt.String()
}

// GetFileContent returns the raw content of a specific file
func (p *Personality) GetFileContent(filename string) (string, bool) {
	content, ok := p.Files[filename]
	return content, ok
}

// HasFile checks if a file was loaded
func (p *Personality) HasFile(filename string) bool {
	_, ok := p.Files[filename]
	return ok
}

// GetName extracts the agent name from IDENTITY.md
func (p *Personality) GetName() string {
	if identity, ok := p.Files["IDENTITY.md"]; ok {
		// Simple extraction: look for "Name:" or "- **Name:**"
		lines := strings.Split(identity, "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.Contains(line, "Name:") {
				// Extract after "Name:"
				parts := strings.SplitN(line, "Name:", 2)
				if len(parts) == 2 {
					name := strings.TrimSpace(parts[1])
					// Remove markdown bold
					name = strings.Trim(name, "* ")
					return name
				}
			}
		}
	}
	return "Штрудель" // Default
}

// GetEmoji extracts the emoji from IDENTITY.md
func (p *Personality) GetEmoji() string {
	if identity, ok := p.Files["IDENTITY.md"]; ok {
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
	return "🕯️" // Default
}

// Reload refreshes all files from disk
func (p *Personality) Reload() error {
	p.Files = make(map[string]string)
	p.Skills = nil
	return p.loadFiles()
}

// discoverSkills scans the skills directory for SKILL.md files
func (p *Personality) discoverSkills() error {
	skillsPath := filepath.Join(p.BasePath, "skills")

	// Check if skills directory exists
	if _, err := os.Stat(skillsPath); os.IsNotExist(err) {
		return nil // Skills directory doesn't exist, not an error
	}

	// Read skills directory
	entries, err := os.ReadDir(skillsPath)
	if err != nil {
		return fmt.Errorf("failed to read skills directory: %w", err)
	}

	// Scan each subdirectory for SKILL.md
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillName := entry.Name()
		skillFilePath := filepath.Join(skillsPath, skillName, "SKILL.md")

		// Check if SKILL.md exists
		content, err := os.ReadFile(skillFilePath)
		if err != nil {
			continue // Skip directories without SKILL.md
		}

		// Extract first non-empty line as description
		description := ""
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "#") {
				description = line
				break
			}
		}

		if description == "" {
			description = "No description available"
		}

		// Add skill entry
		p.Skills = append(p.Skills, SkillEntry{
			Name:        skillName,
			Description: description,
			Path:        skillFilePath,
		})
	}

	return nil
}

// GetMinimalSystemPrompt returns only IDENTITY + SOUL sections (for sub-agents)
func (p *Personality) GetMinimalSystemPrompt() string {
	var prompt strings.Builder

	if identity, ok := p.Files["IDENTITY.md"]; ok {
		prompt.WriteString("## IDENTITY\n\n")
		prompt.WriteString(identity)
		prompt.WriteString("\n\n")
	}

	if soul, ok := p.Files["SOUL.md"]; ok {
		prompt.WriteString("## SOUL\n\n")
		prompt.WriteString(soul)
		prompt.WriteString("\n\n")
	}

	return prompt.String()
}

// GetIdentityLine returns a single identity line (for ultra-minimal sub-agents)
func (p *Personality) GetIdentityLine() string {
	name := p.GetName()
	emoji := p.GetEmoji()
	return fmt.Sprintf("You are %s %s.", name, emoji)
}

// GetSkillsSummary returns a formatted list of available skills
func (p *Personality) GetSkillsSummary() string {
	if len(p.Skills) == 0 {
		return ""
	}

	var summary strings.Builder
	for _, skill := range p.Skills {
		summary.WriteString(fmt.Sprintf("- %s (%s): %s\n", skill.Name, skill.Path, skill.Description))
	}

	return summary.String()
}
