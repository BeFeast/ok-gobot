package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultPath is the canonical default bootstrap directory.
	DefaultPath = "~/ok-gobot-soul"

	// Keep substantially more context from bootstrap files; 8k was truncating
	// MEMORY.md and AGENTS.md in real deployments.
	maxFileChars = 32000
)

// Memory prompt mode names. Mirror the values defined in
// internal/config so callers can reference them without importing config.
const (
	MemoryModeEager          = "eager"
	MemoryModeRetrievalFirst = "retrieval_first"
	MemoryModeStartupRecent  = "startup_recent"
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

var filesToLoad = []string{
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
	Name         string
	Description  string
	Path         string
	UtilityScore int
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
	for _, filename := range filesToLoad {
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

// SystemPrompt builds the markdown bootstrap prompt using the eager memory
// mode (the original behavior: MEMORY.md plus today's and yesterday's daily
// notes are inlined). Equivalent to SystemPromptForMode(MemoryModeEager).
func (l *Loader) SystemPrompt() string {
	return l.SystemPromptForMode(MemoryModeEager)
}

// SystemPromptForMode builds the markdown bootstrap prompt with memory
// injection controlled by mode:
//   - "eager": MEMORY.md + today's + yesterday's daily notes (current default).
//   - "retrieval_first": MEMORY.md only; daily notes are reachable via
//     memory_search/memory_get instead of being inlined.
//   - "startup_recent": MEMORY.md + today's daily note only; yesterday's note
//     is reachable via retrieval.
//
// Identity/personality files (SOUL, IDENTITY, USER, TOOLS, AGENTS, HEARTBEAT)
// are always loaded so the bot retains stable bootstrap context regardless
// of mode.
func (l *Loader) SystemPromptForMode(mode string) string {
	return l.SystemPromptFilteredForMode(mode, nil, nil)
}

// SystemPromptFiltered builds the bootstrap prompt while applying an optional
// memory-source allow function and sanitizer to memory sections.
func (l *Loader) SystemPromptFiltered(allowMemorySource func(source string) bool, sanitizeMemoryContent func(source, content string) string) string {
	return l.SystemPromptFilteredForMode(MemoryModeEager, allowMemorySource, sanitizeMemoryContent)
}

// SystemPromptFilteredForMode builds the bootstrap prompt with both memory mode
// selection and optional source filtering/sanitization.
func (l *Loader) SystemPromptFilteredForMode(mode string, allowMemorySource func(source string) bool, sanitizeMemoryContent func(source, content string) string) string {
	if l == nil {
		return ""
	}
	mode = normalizeMode(mode)

	var prompt strings.Builder

	// Bootstrap order: SOUL -> IDENTITY -> USER -> TOOLS -> AGENTS.
	if soul, ok := l.Files["SOUL.md"]; ok {
		prompt.WriteString("## SOUL\n\n")
		prompt.WriteString(soul)
		prompt.WriteString("\n\n")
	}

	if identity, ok := l.Files["IDENTITY.md"]; ok {
		prompt.WriteString("## IDENTITY\n\n")
		prompt.WriteString(identity)
		prompt.WriteString("\n\n")
	}

	if user, ok := l.Files["USER.md"]; ok {
		prompt.WriteString("## USER CONTEXT\n\n")
		prompt.WriteString(user)
		prompt.WriteString("\n\n")
	}

	if toolsRef, ok := l.Files["TOOLS.md"]; ok {
		prompt.WriteString("## TOOLS REFERENCE\n\n")
		prompt.WriteString(toolsRef)
		prompt.WriteString("\n\n")
	}

	if agents, ok := l.Files["AGENTS.md"]; ok {
		prompt.WriteString("## AGENT PROTOCOL\n\n")
		prompt.WriteString(agents)
		prompt.WriteString("\n\n")
	}

	if heartbeat, ok := l.Files["HEARTBEAT.md"]; ok {
		prompt.WriteString("## HEARTBEAT\n\n")
		prompt.WriteString(heartbeat)
		prompt.WriteString("\n\n")
	}

	if memory, ok := l.Files["MEMORY.md"]; ok && memorySourceAllowed(allowMemorySource, "MEMORY.md") {
		prompt.WriteString("## LONG-TERM MEMORY\n\n")
		prompt.WriteString(sanitizeMemorySection(sanitizeMemoryContent, "MEMORY.md", memory))
		prompt.WriteString("\n\n")
	}

	for _, date := range l.dailyNoteCandidatesForMode(mode) {
		key := "memory/" + date + ".md"
		if note, ok := l.Files[key]; ok && memorySourceAllowed(allowMemorySource, key) {
			prompt.WriteString("## DAILY MEMORY: ")
			prompt.WriteString(date)
			prompt.WriteString("\n\n")
			prompt.WriteString(sanitizeMemorySection(sanitizeMemoryContent, key, note))
			prompt.WriteString("\n\n")
		}
	}

	return prompt.String()
}

// DailyNoteDatesForMode returns the date keys (YYYY-MM-DD) that should be
// inlined into the system prompt for the given mode. Retrieval_first mode
// returns no inline daily notes; startup_recent returns today only; eager
// (default) returns today and yesterday.
//
// Only dates whose corresponding memory/<date>.md exists on disk are
// returned, so the result reflects what will actually appear in the prompt.
func (l *Loader) DailyNoteDatesForMode(mode string) []string {
	if l == nil {
		return nil
	}
	candidates := l.dailyNoteCandidatesForMode(mode)
	out := make([]string, 0, len(candidates))
	for _, d := range candidates {
		if _, ok := l.Files["memory/"+d+".md"]; ok {
			out = append(out, d)
		}
	}
	return out
}

func (l *Loader) dailyNoteCandidatesForMode(mode string) []string {
	switch normalizeMode(mode) {
	case MemoryModeRetrievalFirst:
		return nil
	case MemoryModeStartupRecent:
		return []string{l.currentTime().Format("2006-01-02")}
	default:
		today := l.currentTime().Format("2006-01-02")
		yesterday := l.currentTime().AddDate(0, 0, -1).Format("2006-01-02")
		return []string{today, yesterday}
	}
}

// DailyNoteSourcesForMode returns the relative source paths of daily notes
// the loader has on disk that are NOT inlined under the given mode. These
// are the files an agent should reach for via memory_search/memory_get.
func (l *Loader) DailyNoteSourcesForMode(mode string) []string {
	if l == nil {
		return nil
	}
	inline := map[string]struct{}{}
	for _, d := range l.dailyNoteCandidatesForMode(mode) {
		inline["memory/"+d+".md"] = struct{}{}
	}
	var out []string
	for name := range l.Files {
		if !strings.HasPrefix(name, "memory/") {
			continue
		}
		if _, ok := inline[name]; ok {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// NormalizeMemoryMode normalizes a memory mode string to a canonical value.
// Empty or unknown values fall back to MemoryModeEager so the loader keeps
// its original behavior when configuration is absent.
func NormalizeMemoryMode(mode string) string {
	return normalizeMode(mode)
}

func normalizeMode(mode string) string {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case MemoryModeRetrievalFirst:
		return MemoryModeRetrievalFirst
	case MemoryModeStartupRecent:
		return MemoryModeStartupRecent
	default:
		return MemoryModeEager
	}
}

func memorySourceAllowed(allow func(source string) bool, source string) bool {
	return allow == nil || allow(source)
}

func sanitizeMemorySection(sanitize func(source, content string) string, source, content string) string {
	if sanitize == nil {
		return content
	}
	return sanitize(source, content)
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

// SkillsSummary returns a formatted list of available skills, sorted by utility score descending.
func (l *Loader) SkillsSummary() string {
	if l == nil || len(l.Skills) == 0 {
		return ""
	}

	sorted := make([]SkillEntry, len(l.Skills))
	copy(sorted, l.Skills)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].UtilityScore > sorted[j].UtilityScore
	})

	var summary strings.Builder
	for _, skill := range sorted {
		dir := filepath.Dir(skill.Path)
		summary.WriteString(fmt.Sprintf("- %s (SKILL.md: %s, baseDir: %s, score: %d): %s\n", skill.Name, skill.Path, dir, skill.UtilityScore, skill.Description))
	}

	return summary.String()
}

// ApplyScores sets the UtilityScore on each skill by name from the provided map.
func (l *Loader) ApplyScores(scores map[string]int) {
	if l == nil {
		return
	}
	for i := range l.Skills {
		if score, ok := scores[l.Skills[i].Name]; ok {
			l.Skills[i].UtilityScore = score
		}
	}
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
