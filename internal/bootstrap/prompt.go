package bootstrap

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"ok-gobot/internal/tools"
)

// PromptOptions controls full system prompt assembly.
type PromptOptions struct {
	Mode                   string
	ThinkLevel             string
	MemoryMode             string // "eager" (default), "retrieval_first", or "startup_recent"
	ModelAliases           map[string]string
	Now                    func() time.Time
	MemorySourceAllowed    func(source string) bool
	MemoryContentSanitizer func(source, content string) string
	MemoryPolicySummary    string
}

// BuildPrompt assembles the canonical startup prompt.
func BuildPrompt(loader *Loader, registry *tools.Registry, opts PromptOptions) string {
	var prompt strings.Builder

	mode := opts.Mode
	if mode == "" {
		mode = "full"
	}
	memoryMode := normalizeMode(opts.MemoryMode)

	switch mode {
	case "none":
		prompt.WriteString(loader.IdentityLine())
		prompt.WriteString("\n\n")
	case "minimal":
		prompt.WriteString(loader.MinimalPrompt())
	default:
		prompt.WriteString(loader.SystemPromptFilteredForMode(memoryMode, opts.MemorySourceAllowed, opts.MemoryContentSanitizer))

		skillsSummary := loader.SkillsSummary()
		if skillsSummary != "" {
			prompt.WriteString("\n## Skills\n\n")
			prompt.WriteString("Before replying: scan the available skills below.\n")
			prompt.WriteString("- If exactly one skill clearly applies: read its SKILL.md with the `file` tool, then follow it.\n")
			prompt.WriteString("- If multiple could apply: choose the most specific one, then read/follow it.\n")
			prompt.WriteString("- If none clearly apply: do not read any SKILL.md.\n")
			prompt.WriteString("- In SKILL.md, replace `{baseDir}` with the skill's directory path.\n\n")
			prompt.WriteString("Available skills:\n")
			prompt.WriteString(skillsSummary)
			prompt.WriteString("\n")
		}
	}

	prompt.WriteString("\nYou have access to the following tools:\n\n")
	if registry != nil {
		for _, tool := range registry.List() {
			prompt.WriteString(fmt.Sprintf("Tool: %s\n", tool.Name()))
			prompt.WriteString(fmt.Sprintf("Description: %s\n\n", tool.Description()))
		}
	}

	if mode == "full" {
		prompt.WriteString("\n## Tool Usage Guidelines\n\n")
		prompt.WriteString("You are running on the user's computer with REAL access to all listed tools.\n")
		prompt.WriteString("You CAN and SHOULD use tools to fulfill requests. Never say you \"can't\" do something if a tool exists for it.\n")
		prompt.WriteString("Use the native function calling capability when you need to use tools.\n")
		prompt.WriteString("The system will automatically handle tool execution and return results to you.\n\n")
		prompt.WriteString("## Tool Call Style\n\n")
		prompt.WriteString("Default: do not narrate routine, low-risk tool calls — just call the tool.\n")
		prompt.WriteString("Narrate only when it helps: multi-step work, complex problems, sensitive actions, or when user explicitly asks.\n\n")
		prompt.WriteString("## Reply Style\n\n")
		prompt.WriteString("Default: be concise and direct. Do not apologize for a previous incomplete answer unless the user asks.\n")
		prompt.WriteString("For Telegram-facing replies, use plain text. Do not use Markdown headings, backticks, or bold markers around slash commands, file paths, or links.\n")
		prompt.WriteString("When summarizing remembered facts, prefer 2-5 short bullets over long explanatory paragraphs, and stop after the answer. Do not end with a generic help offer.\n\n")
		prompt.WriteString("## Silent Replies\n\n")
		prompt.WriteString("If you have nothing meaningful to add (e.g. heartbeat poll with no issues, acknowledgment-only situations), reply with exactly: SILENT_REPLY\n")
		prompt.WriteString("The system will suppress this and send nothing to the user.\n\n")

		if registry != nil {
			if _, hasMemorySearch := registry.Get("memory_search"); hasMemorySearch {
				_, hasMemoryGet := registry.Get("memory_get")
				prompt.WriteString(buildMemorySection(memoryMode, loader, hasMemoryGet, opts.MemoryPolicySummary))
			}
		}

		prompt.WriteString("## Reply Tags\n\n")
		prompt.WriteString("To reply to the user's message natively (as a Telegram reply): include [[reply_to_current]] anywhere in your response.\n")
		prompt.WriteString("To reply to a specific message: include [[reply_to:<message_id>]]. Tags are stripped from the final message.\n\n")

		prompt.WriteString("## Reactions\n\n")
		prompt.WriteString("You can react to the user's message with an emoji by including [[react:emoji]] in your response (e.g. [[react:👍]] or [[react:😂]]).\n")
		prompt.WriteString("Use reactions sparingly — only when truly relevant (at most 1 reaction per 5-10 exchanges). The tag is stripped from the final message.\n\n")

		if len(opts.ModelAliases) > 0 {
			prompt.WriteString("## Model Aliases\n")
			prompt.WriteString("Prefer aliases when discussing model overrides with the user:\n")
			for alias, fullName := range opts.ModelAliases {
				prompt.WriteString(fmt.Sprintf("  %s → %s\n", alias, fullName))
			}
			prompt.WriteString("\n")
		}
	}

	if opts.ThinkLevel != "" && opts.ThinkLevel != "off" {
		prompt.WriteString("\n## Reasoning\n\n")
		prompt.WriteString("When solving complex problems, use structured thinking:\n")
		prompt.WriteString("<think>\n[your reasoning process here]\n</think>\n")
		prompt.WriteString("Then provide your final answer directly.\n\n")
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	prompt.WriteString(fmt.Sprintf("Runtime: os=%s arch=%s date=%s\n",
		runtime.GOOS, runtime.GOARCH, now().Format("2006-01-02")))

	return prompt.String()
}

// buildMemorySection emits the "## Memory" guidance block. The text is
// mode-aware so the agent gets clear direction about whether to rely on
// inlined daily notes or to retrieve them on demand.
func buildMemorySection(memoryMode string, loader *Loader, hasMemoryGet bool, policySummary string) string {
	var b strings.Builder
	b.WriteString("## Memory\n\n")
	if policySummary != "" {
		b.WriteString(policySummary)
		b.WriteString("\n")
		b.WriteString("Only use memory sources permitted by this policy. Denied scopes are unavailable.\n\n")
	}

	switch memoryMode {
	case MemoryModeRetrievalFirst:
		b.WriteString("Memory mode: retrieval_first.\n")
		b.WriteString("Daily notes (memory/YYYY-MM-DD.md) are NOT inlined in this prompt — search for them.\n")
		b.WriteString("Before answering anything about prior work, decisions, dates, people, preferences, or todos:\n")
		b.WriteString("1. Call memory_search with a focused query.\n")
		if hasMemoryGet {
			b.WriteString("2. If a result looks relevant, call memory_get with the exact source + header_path for full context.\n")
		}
		b.WriteString("Cite source paths (e.g. memory/2026-04-29.md) when you use retrieved content so the user can verify.\n")
		if loader != nil {
			if hint := dailyNoteHint(loader, MemoryModeRetrievalFirst); hint != "" {
				b.WriteString(hint)
			}
		}
	case MemoryModeStartupRecent:
		b.WriteString("Memory mode: startup_recent.\n")
		b.WriteString("Today's daily note is inlined above; older notes are NOT — search for them.\n")
		b.WriteString("Before answering questions that span beyond today, call memory_search and cite source paths.\n")
		if hasMemoryGet {
			b.WriteString("Use memory_get with source + header_path for exact context when needed.\n")
		}
		if loader != nil {
			if hint := dailyNoteHint(loader, MemoryModeStartupRecent); hint != "" {
				b.WriteString(hint)
			}
		}
	default:
		b.WriteString("Memory mode: eager.\n")
		b.WriteString("Before answering anything about prior work, decisions, dates, people, preferences, or todos:\n")
		b.WriteString("call memory_search first, then use the results to inform your answer.\n")
		if hasMemoryGet {
			b.WriteString("If needed, call memory_get with source + header_path for exact context.\n")
		}
	}
	b.WriteString("\n")
	return b.String()
}

// dailyNoteHint surfaces the names of daily notes the loader has on disk but
// did NOT inline in the system prompt. Listing them helps the agent pick a
// targeted memory_get call without first having to discover the filename.
func dailyNoteHint(loader *Loader, memoryMode string) string {
	sources := loader.DailyNoteSourcesForMode(memoryMode)
	if len(sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Available retrieval-only daily notes: ")
	for i, s := range sources {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(s)
	}
	b.WriteString(".\n")
	return b.String()
}
