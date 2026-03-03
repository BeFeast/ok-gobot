// Package agent implements the AI agent personality and memory system.
package agent

import "ok-gobot/internal/bootstrap"

// SkillEntry represents a discovered skill.
type SkillEntry = bootstrap.SkillEntry

// Personality wraps the canonical bootstrap loader.
type Personality struct {
	BasePath string
	Files    map[string]string
	Skills   []SkillEntry
	loader   *bootstrap.Loader
}

// NewPersonality creates a new personality loader.
func NewPersonality(basePath string) (*Personality, error) {
	loader, err := bootstrap.NewLoader(basePath)
	if err != nil {
		return nil, err
	}

	return &Personality{
		BasePath: loader.BasePath,
		Files:    loader.Files,
		Skills:   loader.Skills,
		loader:   loader,
	}, nil
}

// Loader exposes the canonical bootstrap loader.
func (p *Personality) Loader() *bootstrap.Loader {
	if p == nil {
		return nil
	}
	if p.loader != nil {
		return p.loader
	}
	return &bootstrap.Loader{
		BasePath: p.BasePath,
		Files:    p.Files,
		Skills:   p.Skills,
	}
}

// GetSystemPrompt builds the complete system prompt from all loaded files.
func (p *Personality) GetSystemPrompt() string {
	return p.Loader().SystemPrompt()
}

// GetFileContent returns the raw content of a specific file.
func (p *Personality) GetFileContent(filename string) (string, bool) {
	return p.Loader().FileContent(filename)
}

// HasFile checks if a file was loaded.
func (p *Personality) HasFile(filename string) bool {
	return p.Loader().HasFile(filename)
}

// GetName extracts the agent name from IDENTITY.md.
func (p *Personality) GetName() string {
	return p.Loader().Name()
}

// GetEmoji extracts the emoji from IDENTITY.md.
func (p *Personality) GetEmoji() string {
	return p.Loader().Emoji()
}

// Reload refreshes all files from disk.
func (p *Personality) Reload() error {
	if p == nil {
		return nil
	}

	if p.loader == nil {
		loader, err := bootstrap.NewLoader(p.BasePath)
		if err != nil {
			return err
		}
		p.loader = loader
		p.BasePath = loader.BasePath
		p.Files = loader.Files
		p.Skills = loader.Skills
		return nil
	}

	if err := p.loader.Reload(); err != nil {
		return err
	}
	p.BasePath = p.loader.BasePath
	p.Files = p.loader.Files
	p.Skills = p.loader.Skills
	return nil
}

// GetMinimalSystemPrompt returns only IDENTITY + SOUL sections (for sub-agents).
func (p *Personality) GetMinimalSystemPrompt() string {
	return p.Loader().MinimalPrompt()
}

// GetIdentityLine returns a single identity line (for ultra-minimal sub-agents).
func (p *Personality) GetIdentityLine() string {
	return p.Loader().IdentityLine()
}

// GetSkillsSummary returns a formatted list of available skills.
func (p *Personality) GetSkillsSummary() string {
	return p.Loader().SkillsSummary()
}
