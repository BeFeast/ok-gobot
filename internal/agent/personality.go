// Package agent implements the AI agent personality and memory system.
package agent

import (
	"sort"
	"sync"

	"ok-gobot/internal/bootstrap"
)

// SkillScoreLoader can return the current utility scores for all known skills.
// Implemented by storage.Store.
type SkillScoreLoader interface {
	ListSkillScoreMap() (map[string]int, error)
}

// SkillEntry represents a discovered skill.
type SkillEntry = bootstrap.SkillEntry

// Personality wraps the canonical bootstrap loader.
type Personality struct {
	mu          sync.RWMutex
	BasePath    string
	Files       map[string]string
	Skills      []SkillEntry
	loader      *bootstrap.Loader
	scoreLoader SkillScoreLoader
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

// SetSkillScoreLoader registers a loader that provides utility scores for skills.
// Scores are applied on every Reload so they survive bootstrap re-discovery.
func (p *Personality) SetSkillScoreLoader(loader SkillScoreLoader) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.scoreLoader = loader
	p.mu.Unlock()
}

// EnrichSkillScores applies the given score map to the personality's skills
// and re-sorts them so higher-scored skills appear first.
func (p *Personality) EnrichSkillScores(scores map[string]int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enrichSkillScoresLocked(scores)
}

// enrichSkillScoresLocked must be called with p.mu held.
func (p *Personality) enrichSkillScoresLocked(scores map[string]int) {
	for i := range p.Skills {
		if score, ok := scores[p.Skills[i].Name]; ok {
			p.Skills[i].UtilityScore = score
		}
	}
	sort.Slice(p.Skills, func(i, j int) bool {
		if p.Skills[i].UtilityScore != p.Skills[j].UtilityScore {
			return p.Skills[i].UtilityScore > p.Skills[j].UtilityScore
		}
		return p.Skills[i].Name < p.Skills[j].Name
	})
	// Mirror into loader so SkillsSummary() reflects the updated order.
	if p.loader != nil {
		p.loader.EnrichSkillScores(scores)
	}
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

	// Re-apply scores after re-discovery so they survive filesystem reloads.
	if p.scoreLoader != nil {
		if scores, err := p.scoreLoader.ListSkillScoreMap(); err == nil {
			p.enrichSkillScoresLocked(scores)
		}
	}
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
