package control

import (
	"testing"

	"ok-gobot/internal/agent"
)

func TestIsBootstrapToolPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "soul", path: "SOUL.md", want: true},
		{name: "user with dot slash", path: "./USER.md", want: true},
		{name: "agents", path: "AGENTS.md", want: true},
		{name: "memory root", path: "MEMORY.md", want: true},
		{name: "daily memory", path: "memory/2026-03-05.md", want: true},
		{name: "daily memory windows", path: `memory\\2026-03-05.md`, want: true},
		{name: "daily memory absolute", path: "/tmp/memory/2026-03-05.md", want: true},
		{name: "blank", path: "", want: false},
		{name: "nested soul file", path: "docs/SOUL.md", want: false},
		{name: "invalid daily memory name", path: "memory/today.md", want: false},
		{name: "invalid daily memory date", path: "memory/2026-3-5.md", want: false},
		{name: "nested memory root file", path: "notes/MEMORY.md", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBootstrapToolPath(tt.path); got != tt.want {
				t.Fatalf("isBootstrapToolPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestShouldSuppressBootstrapToolEvent(t *testing.T) {
	suppressed := make(map[string]int)

	bootstrapStart := agent.ToolEvent{
		ToolName: "file",
		Type:     agent.ToolEventStarted,
		Input:    `{"command":"read","path":"SOUL.md"}`,
	}
	if !shouldSuppressBootstrapToolEvent(bootstrapStart, suppressed) {
		t.Fatal("expected bootstrap start event to be suppressed")
	}
	if suppressed["file"] != 1 {
		t.Fatalf("expected pending suppressed count=1, got %d", suppressed["file"])
	}

	bootstrapFinish := agent.ToolEvent{ToolName: "file", Type: agent.ToolEventFinished}
	if !shouldSuppressBootstrapToolEvent(bootstrapFinish, suppressed) {
		t.Fatal("expected bootstrap finish event to be suppressed")
	}
	if _, ok := suppressed["file"]; ok {
		t.Fatal("expected suppressed counter to be cleared after matching finish")
	}

	normalStart := agent.ToolEvent{
		ToolName: "search",
		Type:     agent.ToolEventStarted,
		Input:    `{"query":"weather"}`,
	}
	if shouldSuppressBootstrapToolEvent(normalStart, suppressed) {
		t.Fatal("did not expect non-bootstrap start event to be suppressed")
	}

	normalFinish := agent.ToolEvent{ToolName: "search", Type: agent.ToolEventFinished}
	if shouldSuppressBootstrapToolEvent(normalFinish, suppressed) {
		t.Fatal("did not expect non-bootstrap finish event to be suppressed")
	}

	malformed := agent.ToolEvent{
		ToolName: "file",
		Type:     agent.ToolEventStarted,
		Input:    "{bad json",
	}
	if shouldSuppressBootstrapToolEvent(malformed, suppressed) {
		t.Fatal("did not expect malformed tool input to be suppressed")
	}
}
