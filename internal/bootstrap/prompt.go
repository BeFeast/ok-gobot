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
	Mode         string
	ThinkLevel   string
	ModelAliases map[string]string
	Now          func() time.Time
}

// BuildPrompt assembles the canonical startup prompt.
func BuildPrompt(loader *Loader, registry *tools.Registry, opts PromptOptions) string {
	var prompt strings.Builder

	mode := opts.Mode
	if mode == "" {
		mode = "full"
	}

	switch mode {
	case "none":
		prompt.WriteString(loader.IdentityLine())
		prompt.WriteString("\n\n")
	case "minimal":
		prompt.WriteString(loader.MinimalPrompt())
	default:
		prompt.WriteString(loader.SystemPrompt())

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
		prompt.WriteString("## Silent Replies\n\n")
		prompt.WriteString("If you have nothing meaningful to add (e.g. heartbeat poll with no issues, acknowledgment-only situations), reply with exactly: SILENT_REPLY\n")
		prompt.WriteString("The system will suppress this and send nothing to the user.\n\n")

		if registry != nil {
			_, hasMemorySearch := registry.Get("memory_search")
			_, hasMemoryGet := registry.Get("memory_get")
			_, hasLegacyMemory := registry.Get("memory")
			if hasMemorySearch || hasMemoryGet || hasLegacyMemory {
				prompt.WriteString("## Memory\n\n")
				prompt.WriteString("Before answering anything about prior work, decisions, dates, people, preferences, or todos:\n")
				prompt.WriteString("proactively call memory_search first, then call memory_get for surrounding context when needed.\n")
				if hasLegacyMemory && (!hasMemorySearch || !hasMemoryGet) {
					prompt.WriteString("If memory_search/memory_get are unavailable, use the legacy memory tool search command as fallback.\n")
				}
				prompt.WriteString("\n")
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
