package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/tools"
)

func TestLoaderBuildsSystemPromptFromFiles(t *testing.T) {
	today := time.Date(2026, time.March, 3, 12, 0, 0, 0, time.UTC)
	basePath := t.TempDir()

	writeTestFile(t, filepath.Join(basePath, "IDENTITY.md"), "# Identity\nName: TestBot\nEmoji: 🤖")
	writeTestFile(t, filepath.Join(basePath, "SOUL.md"), "Soul line")
	writeTestFile(t, filepath.Join(basePath, "USER.md"), "User line")
	writeTestFile(t, filepath.Join(basePath, "AGENTS.md"), "Agents line")
	writeTestFile(t, filepath.Join(basePath, "TOOLS.md"), "Tools line")
	writeTestFile(t, filepath.Join(basePath, "HEARTBEAT.md"), "Heartbeat line")
	writeTestFile(t, filepath.Join(basePath, "MEMORY.md"), "Memory line")
	writeTestFile(t, filepath.Join(basePath, "memory", today.Format("2006-01-02")+".md"), "Today line")
	writeTestFile(t, filepath.Join(basePath, "skills", "alpha", "SKILL.md"), "---\ndescription: test skill\n---\n# Title\n")

	loader, err := newLoader(basePath, func() time.Time { return today })
	if err != nil {
		t.Fatalf("newLoader() error = %v", err)
	}

	expected := "" +
		"## SOUL\n\nSoul line\n\n" +
		"## IDENTITY\n\n# Identity\nName: TestBot\nEmoji: 🤖\n\n" +
		"## USER CONTEXT\n\nUser line\n\n" +
		"## TOOLS REFERENCE\n\nTools line\n\n" +
		"## AGENT PROTOCOL\n\nAgents line\n\n" +
		"## HEARTBEAT\n\nHeartbeat line\n\n" +
		"## LONG-TERM MEMORY\n\nMemory line\n\n" +
		"## DAILY MEMORY: 2026-03-03\n\nToday line\n\n"

	if got := loader.SystemPrompt(); got != expected {
		t.Fatalf("SystemPrompt() mismatch\nwant:\n%s\ngot:\n%s", expected, got)
	}
	if !loader.HasFile("MEMORY.md") {
		t.Fatalf("MEMORY.md should be loaded into bootstrap files")
	}
	if got := loader.SystemPrompt(); !strings.Contains(got, "Memory line") {
		t.Fatalf("SystemPrompt() should contain MEMORY.md content")
	}
	if got := loader.SystemPrompt(); !strings.Contains(got, "Today line") {
		t.Fatalf("SystemPrompt() should contain daily memory content")
	}

	if got := loader.MinimalPrompt(); got != "## IDENTITY\n\n# Identity\nName: TestBot\nEmoji: 🤖\n\n## SOUL\n\nSoul line\n\n" {
		t.Fatalf("MinimalPrompt() mismatch: %q", got)
	}

	if got := loader.IdentityLine(); got != "You are TestBot 🤖." {
		t.Fatalf("IdentityLine() = %q", got)
	}

	if got := loader.SkillsSummary(); got == "" {
		t.Fatalf("SkillsSummary() is empty")
	}
}

func TestBuildPromptPreservesFullPromptSections(t *testing.T) {
	loader := &Loader{
		Files: map[string]string{
			"IDENTITY.md": "- **Name:** TestBot\n- **Emoji:** 🤖",
			"SOUL.md":     "Soul line",
		},
		now: func() time.Time {
			return time.Date(2026, time.March, 3, 9, 0, 0, 0, time.UTC)
		},
	}

	registry := tools.NewRegistry()
	registry.Register(testTool{name: "memory_search", desc: "Memory search tool"})

	got := BuildPrompt(loader, registry, PromptOptions{
		Mode:       "full",
		ThinkLevel: "high",
		Now: func() time.Time {
			return time.Date(2026, time.March, 3, 9, 0, 0, 0, time.UTC)
		},
	})

	expected := "" +
		"## SOUL\n\nSoul line\n\n" +
		"## IDENTITY\n\n- **Name:** TestBot\n- **Emoji:** 🤖\n\n" +
		"\nYou have access to the following tools:\n\n" +
		"Tool: memory_search\nDescription: Memory search tool\n\n" +
		"\n## Tool Usage Guidelines\n\n" +
		"You are running on the user's computer with REAL access to all listed tools.\n" +
		"You CAN and SHOULD use tools to fulfill requests. Never say you \"can't\" do something if a tool exists for it.\n" +
		"Use the native function calling capability when you need to use tools.\n" +
		"The system will automatically handle tool execution and return results to you.\n\n" +
		"## Tool Call Style\n\n" +
		"Default: do not narrate routine, low-risk tool calls — just call the tool.\n" +
		"Narrate only when it helps: multi-step work, complex problems, sensitive actions, or when user explicitly asks.\n\n" +
		"## Silent Replies\n\n" +
		"If you have nothing meaningful to add (e.g. heartbeat poll with no issues, acknowledgment-only situations), reply with exactly: SILENT_REPLY\n" +
		"The system will suppress this and send nothing to the user.\n\n" +
		"## Memory\n\n" +
		"Before answering anything about prior work, decisions, dates, people, preferences, or todos:\n" +
		"call memory_search first, then use the results to inform your answer.\n\n" +
		"## Reply Tags\n\n" +
		"To reply to the user's message natively (as a Telegram reply): include [[reply_to_current]] anywhere in your response.\n" +
		"To reply to a specific message: include [[reply_to:<message_id>]]. Tags are stripped from the final message.\n\n" +
		"## Reactions\n\n" +
		"You can react to the user's message with an emoji by including [[react:emoji]] in your response (e.g. [[react:👍]] or [[react:😂]]).\n" +
		"Use reactions sparingly — only when truly relevant (at most 1 reaction per 5-10 exchanges). The tag is stripped from the final message.\n\n" +
		"\n## Reasoning\n\n" +
		"When solving complex problems, use structured thinking:\n" +
		"<think>\n[your reasoning process here]\n</think>\n" +
		"Then provide your final answer directly.\n\n" +
		"Runtime: os=" + runtime.GOOS + " arch=" + runtime.GOARCH + " date=2026-03-03\n"

	if got != expected {
		t.Fatalf("BuildPrompt() mismatch\nwant:\n%s\ngot:\n%s", expected, got)
	}
}

func TestScaffoldCreatesCanonicalBootstrapLayout(t *testing.T) {
	basePath := filepath.Join(t.TempDir(), "bootstrap")

	report, err := Scaffold(basePath)
	if err != nil {
		t.Fatalf("Scaffold() error = %v", err)
	}

	if len(report.CreatedFiles) == 0 {
		t.Fatalf("Scaffold() did not create files")
	}

	for _, filename := range ManagedFiles() {
		if _, err := os.Stat(filepath.Join(basePath, filename)); err != nil {
			t.Fatalf("missing scaffolded file %s: %v", filename, err)
		}
	}

	for _, dir := range []string{"memory", "chrome-profile"} {
		if _, err := os.Stat(filepath.Join(basePath, dir)); err != nil {
			t.Fatalf("missing scaffolded dir %s: %v", dir, err)
		}
	}
}

type testTool struct {
	name string
	desc string
}

func (t testTool) Name() string { return t.name }

func (t testTool) Description() string { return t.desc }

func (t testTool) Execute(_ context.Context, _ ...string) (string, error) {
	return "", nil
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
