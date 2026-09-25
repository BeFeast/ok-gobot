package bot

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/bootstrap"
)

// hiddenBuiltinCommandNames are slash commands that have handlers but no menu
// entry. They are reserved so a skill command can never shadow them.
var hiddenBuiltinCommandNames = []string{
	"start",
	"roles",
	"role",
	"role_run",
	"jobs",
	"job",
	"job_cancel",
	"skill_suggest",
}

// reservedCommandNames returns every built-in command name (menu + hidden).
func (b *Bot) reservedCommandNames() []string {
	builtin := b.builtinCommands()
	names := make([]string, 0, len(builtin)+len(hiddenBuiltinCommandNames))
	for _, cmd := range builtin {
		names = append(names, cmd.Text)
	}
	names = append(names, hiddenBuiltinCommandNames...)
	return names
}

// resolveSkillCommands computes the skill-backed menu commands from the current
// personality snapshot. Skipped skills are logged one line each; nothing here
// can fail the bot.
func (b *Bot) resolveSkillCommands() []bootstrap.SkillCommand {
	if b.personality == nil {
		return nil
	}
	loader := b.personality.Loader()
	if loader == nil {
		return nil
	}
	budget := bootstrap.MaxTelegramCommands - len(b.builtinCommands())
	if budget < 0 {
		budget = 0
	}
	commands, skips := bootstrap.ResolveSkillCommands(loader.Skills, b.reservedCommandNames(), budget)
	for _, skip := range skips {
		log.Printf("[skills] command skipped: skill=%s command=%q reason=%s", skip.SkillName, skip.Command, skip.Reason)
	}
	return commands
}

// setSkillCommands replaces the active skill command snapshot.
func (b *Bot) setSkillCommands(commands []bootstrap.SkillCommand) {
	snapshot := make(map[string]bootstrap.SkillCommand, len(commands))
	for _, cmd := range commands {
		snapshot[cmd.Command] = cmd
	}
	b.skillCommandsMu.Lock()
	b.skillCommands = snapshot
	b.skillCommandsMu.Unlock()
}

// lookupSkillCommand returns the skill command registered under name.
func (b *Bot) lookupSkillCommand(name string) (bootstrap.SkillCommand, bool) {
	b.skillCommandsMu.RLock()
	defer b.skillCommandsMu.RUnlock()
	cmd, ok := b.skillCommands[name]
	return cmd, ok
}

// skillCommandList returns the active skill commands sorted by command name.
func (b *Bot) skillCommandList() []bootstrap.SkillCommand {
	b.skillCommandsMu.RLock()
	list := make([]bootstrap.SkillCommand, 0, len(b.skillCommands))
	for _, cmd := range b.skillCommands {
		list = append(list, cmd)
	}
	b.skillCommandsMu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].Command < list[j].Command })
	return list
}

// parseSlashCommand splits "/name[@bot] args" into the command name and the
// trimmed argument text. It returns ok=false for non-command text and for
// commands addressed to a different bot.
func parseSlashCommand(text, botUsername string) (name, args string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || len(text) < 2 {
		return "", "", false
	}
	head := text[1:]
	rest := ""
	if idx := strings.IndexAny(head, " \t\r\n"); idx >= 0 {
		head, rest = head[:idx], head[idx:]
	}
	if at := strings.Index(head, "@"); at >= 0 {
		target := head[at+1:]
		head = head[:at]
		if botUsername != "" && !strings.EqualFold(target, botUsername) {
			return "", "", false
		}
	}
	if head == "" {
		return "", "", false
	}
	return head, strings.TrimSpace(rest), true
}

// matchSkillCommand reports whether text invokes one of the registered skill
// commands and returns the command with its argument text.
func (b *Bot) matchSkillCommand(text string) (bootstrap.SkillCommand, string, bool) {
	botUsername := ""
	if b.api != nil && b.api.Me != nil {
		botUsername = b.api.Me.Username
	}
	name, args, ok := parseSlashCommand(text, botUsername)
	if !ok {
		return bootstrap.SkillCommand{}, "", false
	}
	cmd, found := b.lookupSkillCommand(name)
	if !found {
		return bootstrap.SkillCommand{}, "", false
	}
	return cmd, args, true
}

// skillCommandPrompt builds the agent-turn content for an explicit skill
// invocation: the skill is pre-selected, so the model reads its SKILL.md and
// follows it for the user's request instead of routing among all skills.
func skillCommandPrompt(cmd bootstrap.SkillCommand, args string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The user invoked the Telegram command /%s, which selects the skill `%s`.\n", cmd.Command, cmd.SkillName)
	fmt.Fprintf(&sb, "Read %s with the `file` tool and follow that skill for this request. Do not consider other skills.\n", cmd.SkillPath)
	fmt.Fprintf(&sb, "In SKILL.md, replace `{baseDir}` with %s.\n\n", filepath.Dir(cmd.SkillPath))
	sb.WriteString("User request:\n")
	sb.WriteString(strings.TrimSpace(args))
	return sb.String()
}

// skillCommandMenuEntries converts skill commands into Telegram menu entries.
func skillCommandMenuEntries(commands []bootstrap.SkillCommand) []telebot.Command {
	entries := make([]telebot.Command, 0, len(commands))
	for _, cmd := range commands {
		entries = append(entries, telebot.Command{Text: cmd.Command, Description: cmd.Description})
	}
	return entries
}
