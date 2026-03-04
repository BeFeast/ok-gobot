// Package agent implements the AI agent personality and memory system.
package agent

import (
	"sync"

	"ok-gobot/internal/bootstrap"
)

// SkillEntry represents a discovered skill.
type SkillEntry = bootstrap.SkillEntry

// Personality wraps the canonical bootstrap loader.
type Personality struct {
	mu       sync.RWMutex
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
		Files:    cloneFiles(loader.Files),
		Skills:   cloneSkills(loader.Skills),
		loader:   loader,
	}, nil
}

// Loader exposes the canonical bootstrap loader snapshot.
func (p *Personality) Loader() *bootstrap.Loader {
	if p == nil {
		return nil
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.loaderSnapshotLocked()
}

// GetSystemPrompt builds the complete system prompt from all loaded files.
func (p *Personality) GetSystemPrompt() string {
	loader := p.Loader()
	if loader == nil {
		return ""
	}
	return loader.SystemPrompt()
}

// GetFileContent returns the raw content of a specific file.
func (p *Personality) GetFileContent(filename string) (string, bool) {
	loader := p.Loader()
	if loader == nil {
		return "", false
	}
	return loader.FileContent(filename)
}

// HasFile checks if a file was loaded.
func (p *Personality) HasFile(filename string) bool {
	loader := p.Loader()
	if loader == nil {
		return false
	}
	return loader.HasFile(filename)
}

// GetName extracts the agent name from IDENTITY.md.
func (p *Personality) GetName() string {
	loader := p.Loader()
	if loader == nil {
		return "Штрудель"
	}
	return loader.Name()
}

// GetEmoji extracts the emoji from IDENTITY.md.
func (p *Personality) GetEmoji() string {
	loader := p.Loader()
	if loader == nil {
		return "🕯️"
	}
	return loader.Emoji()
}

// Reload refreshes all files from disk.
func (p *Personality) Reload() error {
	if p == nil {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.loader == nil {
		loader, err := bootstrap.NewLoader(p.BasePath)
		if err != nil {
			return err
		}
		p.loader = loader
	} else {
		if err := p.loader.Reload(); err != nil {
			return err
		}
	}

	p.BasePath = p.loader.BasePath
	p.Files = cloneFiles(p.loader.Files)
	p.Skills = cloneSkills(p.loader.Skills)
	return nil
}

// GetMinimalSystemPrompt returns only IDENTITY + SOUL sections (for sub-agents).
func (p *Personality) GetMinimalSystemPrompt() string {
	loader := p.Loader()
	if loader == nil {
		return ""
	}
	return loader.MinimalPrompt()
}

// GetIdentityLine returns a single identity line (for ultra-minimal sub-agents).
func (p *Personality) GetIdentityLine() string {
	loader := p.Loader()
	if loader == nil {
		return "You are Штрудель 🕯️."
	}
	return loader.IdentityLine()
}

// GetSkillsSummary returns a formatted list of available skills.
func (p *Personality) GetSkillsSummary() string {
	loader := p.Loader()
	if loader == nil {
		return ""
	}
	return loader.SkillsSummary()
}

func (p *Personality) loaderSnapshotLocked() *bootstrap.Loader {
	if p == nil {
		return nil
	}

	basePath := p.BasePath
	files := cloneFiles(p.Files)
	skills := cloneSkills(p.Skills)
	if p.loader != nil {
		basePath = p.loader.BasePath
		files = cloneFiles(p.loader.Files)
		skills = cloneSkills(p.loader.Skills)
	}

	return &bootstrap.Loader{
		BasePath: basePath,
		Files:    files,
		Skills:   skills,
	}
}

func cloneFiles(src map[string]string) map[string]string {
	if len(src) == 0 {
		return map[string]string{}
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneSkills(src []SkillEntry) []SkillEntry {
	if len(src) == 0 {
		return nil
	}
	dst := make([]SkillEntry, len(src))
	copy(dst, src)
	return dst
}
