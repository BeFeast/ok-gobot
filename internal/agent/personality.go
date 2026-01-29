// Package agent implements the AI agent personality and memory system
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Personality loads and manages agent context files
type Personality struct {
	BasePath string
	Files    map[string]string // filename -> content
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
		p.Files[filename] = string(content)
	}

	// Load daily memory files
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	for _, date := range []string{today, yesterday} {
		path := filepath.Join(p.BasePath, "memory", date+".md")
		content, err := os.ReadFile(path)
		if err == nil {
			p.Files["memory/"+date+".md"] = string(content)
		}
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
	return p.loadFiles()
}
