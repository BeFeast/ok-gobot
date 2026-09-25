package bootstrap

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// MaxTelegramCommands is the Bot API limit for setMyCommands.
	MaxTelegramCommands = 100
	// maxCommandDescriptionRunes is the Bot API limit for a command description.
	maxCommandDescriptionRunes = 256
)

var skillCommandNamePattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// SkillCommand is a Telegram slash command backed by an installed skill.
type SkillCommand struct {
	Command     string
	Description string
	SkillName   string
	SkillPath   string // absolute SKILL.md path
}

// SkillCommandSkip explains why a skill did not get a menu command.
type SkillCommandSkip struct {
	SkillName string
	Command   string
	Reason    string
}

// DefaultSkillCommand derives the Telegram command name from a skill name:
// lower-case with dashes turned into underscores (media-request -> media_request).
func DefaultSkillCommand(skillName string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(skillName)), "-", "_")
}

// ValidSkillCommandName reports whether name satisfies Telegram's command
// syntax: 1-32 characters from [a-z0-9_].
func ValidSkillCommandName(name string) bool {
	return skillCommandNamePattern.MatchString(name)
}

// TruncateCommandDescription squeezes whitespace and cuts the text to the
// Telegram description limit on a word boundary. An empty description falls
// back to a generic label so the command is never rejected.
func TruncateCommandDescription(description, skillName string) string {
	text := strings.Join(strings.Fields(description), " ")
	if text == "" || text == "No description available" {
		text = fmt.Sprintf("Run skill %s", skillName)
	}
	if utf8.RuneCountInString(text) <= maxCommandDescriptionRunes {
		return text
	}
	runes := []rune(text)
	cut := runes[:maxCommandDescriptionRunes-1]
	if idx := strings.LastIndex(string(cut), " "); idx > maxCommandDescriptionRunes/2 {
		cut = []rune(string(cut)[:idx])
	}
	return strings.TrimRight(string(cut), " ,;:") + "…"
}

// ResolveSkillCommands turns installed skills into Telegram menu commands.
// Skills are processed in name order so collisions resolve deterministically.
// A skill is skipped (never fatal) when it is blocked, opted out, has an
// invalid command name, collides with a reserved built-in or another skill, or
// would exceed budget (the remaining room under MaxTelegramCommands).
func ResolveSkillCommands(skills []SkillEntry, reserved []string, budget int) ([]SkillCommand, []SkillCommandSkip) {
	taken := make(map[string]string, len(reserved))
	for _, name := range reserved {
		taken[name] = ""
	}

	sorted := make([]SkillEntry, len(skills))
	copy(sorted, skills)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var commands []SkillCommand
	var skips []SkillCommandSkip
	for _, skill := range sorted {
		if skill.Compatibility == SkillCompatibilityBlocked {
			skips = append(skips, SkillCommandSkip{skill.Name, skill.Command, "skill is blocked: " + skill.CompatibilityReason})
			continue
		}
		if skill.Command == "" {
			skips = append(skips, SkillCommandSkip{skill.Name, "", "opted out via command: \"\""})
			continue
		}
		if !ValidSkillCommandName(skill.Command) {
			skips = append(skips, SkillCommandSkip{skill.Name, skill.Command, "invalid command name (allowed: [a-z0-9_]{1,32})"})
			continue
		}
		if owner, exists := taken[skill.Command]; exists {
			reason := "collides with a built-in command"
			if owner != "" {
				reason = "collides with skill " + owner
			}
			skips = append(skips, SkillCommandSkip{skill.Name, skill.Command, reason})
			continue
		}
		if len(commands) >= budget {
			skips = append(skips, SkillCommandSkip{skill.Name, skill.Command, fmt.Sprintf("command menu limit of %d reached", MaxTelegramCommands)})
			continue
		}
		taken[skill.Command] = skill.Name
		commands = append(commands, SkillCommand{
			Command:     skill.Command,
			Description: TruncateCommandDescription(skill.CommandDescription, skill.Name),
			SkillName:   skill.Name,
			SkillPath:   skill.Path,
		})
	}
	return commands, skips
}
