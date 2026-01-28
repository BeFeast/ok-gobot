// Package agent implements the AI agent personality and memory system
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Personality holds the agent's identity and behavior configuration
type Personality struct {
	// Identity
	Name        string
	Creature    string
	Vibe        string
	Emoji       string
	OriginStory string

	// Soul - Core truths and behavior
	CoreTruths []string
	Boundaries []string
	VibeNotes  string

	// User context
	User *UserProfile

	// Files location
	BasePath string
}

// UserProfile holds information about the human
type UserProfile struct {
	Name      string
	CallMe    string
	Age       int
	Location  string
	Timezone  string
	Languages []string

	// Contacts
	Contacts map[string]string // type -> address

	// Family
	Family map[string]string // relation -> description

	// Work
	WorkRole    string
	WorkTenure  string
	WorkCompany string

	// Health
	HealthConditions []string
	Doctor           string
	Medications      []string

	// Finance
	MonthlyIncome   int
	MonthlyDebt     int
	TotalDebt       int
	PrimaryCalendar string

	// Projects
	Projects []Project

	// Preferences
	Preferences map[string]string
}

// Project represents a user project
type Project struct {
	Name        string
	Description string
	Location    string
}

// NewPersonality creates a new personality by loading files from the agent directory
func NewPersonality(basePath string) (*Personality, error) {
	if basePath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		basePath = filepath.Join(homeDir, "clawd")
	}

	p := &Personality{
		BasePath: basePath,
		User:     &UserProfile{},
	}

	// Load identity
	if err := p.loadIdentity(); err != nil {
		return nil, fmt.Errorf("failed to load identity: %w", err)
	}

	// Load soul
	if err := p.loadSoul(); err != nil {
		return nil, fmt.Errorf("failed to load soul: %w", err)
	}

	// Load user profile
	if err := p.loadUser(); err != nil {
		return nil, fmt.Errorf("failed to load user: %w", err)
	}

	return p, nil
}

// loadIdentity reads IDENTITY.md
func (p *Personality) loadIdentity() error {
	content, err := os.ReadFile(filepath.Join(p.BasePath, "IDENTITY.md"))
	if err != nil {
		// Use defaults if file doesn't exist
		p.Name = "Штрудель"
		p.Creature = "AI familiar"
		p.Vibe = "Casual, weird, technically sharp"
		p.Emoji = "🕯️"
		return nil
	}

	// Simple parsing - extract key values
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "- **Name:**") {
			p.Name = extractValue(line, "Name:")
		} else if strings.HasPrefix(line, "- **Creature:**") {
			p.Creature = extractValue(line, "Creature:")
		} else if strings.HasPrefix(line, "- **Vibe:**") {
			p.Vibe = extractValue(line, "Vibe:")
		} else if strings.HasPrefix(line, "- **Emoji:**") {
			p.Emoji = strings.TrimSpace(strings.TrimPrefix(line, "- **Emoji:**"))
		}
	}

	return nil
}

// loadSoul reads SOUL.md
func (p *Personality) loadSoul() error {
	content, err := os.ReadFile(filepath.Join(p.BasePath, "SOUL.md"))
	if err != nil {
		// Use defaults
		p.CoreTruths = []string{
			"Be genuinely helpful, not performatively helpful",
			"Have opinions",
			"Be resourceful before asking",
		}
		return nil
	}

	// Extract core truths section
	lines := strings.Split(string(content), "\n")
	inCoreTruths := false
	for _, line := range lines {
		if strings.Contains(line, "## Core Truths") {
			inCoreTruths = true
			continue
		}
		if inCoreTruths && strings.HasPrefix(line, "## ") {
			break
		}
		if inCoreTruths && strings.HasPrefix(line, "**") {
			truth := strings.TrimPrefix(line, "**")
			truth = strings.Split(truth, "**")[0]
			p.CoreTruths = append(p.CoreTruths, truth)
		}
	}

	return nil
}

// loadUser reads USER.md
func (p *Personality) loadUser() error {
	content, err := os.ReadFile(filepath.Join(p.BasePath, "USER.md"))
	if err != nil {
		return nil // Optional file
	}

	// Parse user info (simplified)
	lines := strings.Split(string(content), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)

		if strings.Contains(line, "**Name:**") {
			p.User.Name = extractValue(line, "Name:")
		} else if strings.Contains(line, "**Call me:**") {
			p.User.CallMe = extractValue(line, "Call me:")
		} else if strings.Contains(line, "**Age:**") {
			fmt.Sscanf(extractValue(line, "Age:"), "%d", &p.User.Age)
		} else if strings.Contains(line, "**Location:**") {
			p.User.Location = extractValue(line, "Location:")
		} else if strings.Contains(line, "**Timezone:**") {
			p.User.Timezone = extractValue(line, "Timezone:")
		}

		// Parse contacts table
		if strings.Contains(line, "Telegram") && i+1 < len(lines) {
			if p.User.Contacts == nil {
				p.User.Contacts = make(map[string]string)
			}
			parts := strings.Split(line, "|")
			if len(parts) >= 3 {
				p.User.Contacts["telegram"] = strings.TrimSpace(parts[2])
			}
		}
	}

	return nil
}

// extractValue extracts a value from a markdown line
func extractValue(line, key string) string {
	parts := strings.SplitN(line, key, 2)
	if len(parts) < 2 {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(parts[1], ":"))
}

// GetSystemPrompt generates a system prompt for the AI
func (p *Personality) GetSystemPrompt() string {
	var prompt strings.Builder

	prompt.WriteString(fmt.Sprintf("You are %s (%s). %s\n\n", p.Name, p.Creature, p.Emoji))
	prompt.WriteString(fmt.Sprintf("Vibe: %s\n\n", p.Vibe))

	prompt.WriteString("Core Truths:\n")
	for _, truth := range p.CoreTruths {
		prompt.WriteString(fmt.Sprintf("- %s\n", truth))
	}

	if p.User != nil && p.User.Name != "" {
		prompt.WriteString(fmt.Sprintf("\nYou are helping %s (call them %s).\n", p.User.Name, p.User.CallMe))
		if p.User.Location != "" {
			prompt.WriteString(fmt.Sprintf("They live in %s.\n", p.User.Location))
		}
	}

	return prompt.String()
}

// LoadMemoryFiles returns list of memory files to read on startup
func (p *Personality) LoadMemoryFiles() []string {
	var files []string

	// Core personality files
	files = append(files,
		filepath.Join(p.BasePath, "SOUL.md"),
		filepath.Join(p.BasePath, "IDENTITY.md"),
		filepath.Join(p.BasePath, "USER.md"),
	)

	// Daily memory for today and yesterday
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")

	files = append(files,
		filepath.Join(p.BasePath, "memory", today+".md"),
		filepath.Join(p.BasePath, "memory", yesterday+".md"),
	)

	return files
}
