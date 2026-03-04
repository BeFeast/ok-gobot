package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/tools"
)

type staticTool struct {
	name string
	desc string
}

func (t *staticTool) Name() string        { return t.name }
func (t *staticTool) Description() string { return t.desc }
func (t *staticTool) Execute(ctx context.Context, args ...string) (string, error) {
	return "", nil
}

func writeTestFile(t *testing.T, baseDir, name, content string) {
	t.Helper()
	path := filepath.Join(baseDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}
}

func TestNewPersonality_DoesNotLoadMemoryMD(t *testing.T) {
	tmp := t.TempDir()
	writeTestFile(t, tmp, "MEMORY.md", "private memory that must not be eagerly loaded")

	p, err := NewPersonality(tmp)
	if err != nil {
		t.Fatalf("NewPersonality() error = %v", err)
	}

	if p.HasFile("MEMORY.md") {
		t.Fatalf("MEMORY.md should not be loaded into personality files")
	}
}

func TestGetSystemPrompt_ExcludesMemoryAndUsesBootstrapOrder(t *testing.T) {
	tmp := t.TempDir()

	writeTestFile(t, tmp, "SOUL.md", "SOUL_SECTION")
	writeTestFile(t, tmp, "IDENTITY.md", "IDENTITY_SECTION")
	writeTestFile(t, tmp, "USER.md", "USER_SECTION")
	writeTestFile(t, tmp, "TOOLS.md", "TOOLS_SECTION")
	writeTestFile(t, tmp, "AGENTS.md", "AGENTS_SECTION")
	writeTestFile(t, tmp, "MEMORY.md", "MEMORY_SECRET_SHOULD_NOT_APPEAR")

	today := time.Now().Format("2006-01-02")
	writeTestFile(t, tmp, filepath.Join("memory", today+".md"), "TODAY_MEMORY_SHOULD_NOT_APPEAR")

	p, err := NewPersonality(tmp)
	if err != nil {
		t.Fatalf("NewPersonality() error = %v", err)
	}

	prompt := p.GetSystemPrompt()
	if strings.Contains(prompt, "MEMORY_SECRET_SHOULD_NOT_APPEAR") {
		t.Fatalf("system prompt should not contain MEMORY.md content")
	}
	if strings.Contains(prompt, "TODAY_MEMORY_SHOULD_NOT_APPEAR") {
		t.Fatalf("system prompt should not contain daily memory content")
	}
	if strings.Contains(prompt, "## LONG-TERM MEMORY") {
		t.Fatalf("system prompt should not include LONG-TERM MEMORY section")
	}

	sections := []string{
		"## SOUL",
		"## IDENTITY",
		"## USER CONTEXT",
		"## TOOLS REFERENCE",
		"## AGENT PROTOCOL",
	}

	lastPos := -1
	for _, section := range sections {
		pos := strings.Index(prompt, section)
		if pos == -1 {
			t.Fatalf("missing section %q in system prompt", section)
		}
		if pos <= lastPos {
			t.Fatalf("section %q is out of order in system prompt", section)
		}
		lastPos = pos
	}
}

func TestBuildSystemPrompt_OrdersSkillsAfterAgentsAndMentionsMemoryTools(t *testing.T) {
	registry := tools.NewRegistry()
	registry.Register(&staticTool{name: "memory", desc: "legacy memory tool"})

	personality := &Personality{
		Files: map[string]string{
			"SOUL.md":     "SOUL_SECTION",
			"IDENTITY.md": "IDENTITY_SECTION",
			"USER.md":     "USER_SECTION",
			"TOOLS.md":    "TOOLS_SECTION",
			"AGENTS.md":   "AGENTS_SECTION",
		},
		Skills: []SkillEntry{
			{
				Name:        "repo-inspector",
				Description: "Inspect repository quickly",
				Path:        "/tmp/skills/repo-inspector/SKILL.md",
			},
		},
	}

	agent := NewToolCallingAgent(nil, registry, personality)
	prompt := agent.buildSystemPrompt()

	sections := []string{
		"## SOUL",
		"## IDENTITY",
		"## USER CONTEXT",
		"## TOOLS REFERENCE",
		"## AGENT PROTOCOL",
		"## Skills",
	}

	lastPos := -1
	for _, section := range sections {
		pos := strings.Index(prompt, section)
		if pos == -1 {
			t.Fatalf("missing section %q in prompt", section)
		}
		if pos <= lastPos {
			t.Fatalf("section %q is out of order in prompt", section)
		}
		lastPos = pos
	}

	if !strings.Contains(prompt, "memory_search") {
		t.Fatalf("prompt should instruct proactive use of memory_search")
	}
	if !strings.Contains(prompt, "memory_get") {
		t.Fatalf("prompt should instruct proactive use of memory_get")
	}
}
