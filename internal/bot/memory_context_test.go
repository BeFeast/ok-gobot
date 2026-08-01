package bot

import (
	"strings"
	"testing"

	"ok-gobot/internal/memory"
)

func TestFormatMemoryContextFooterExplainsSources(t *testing.T) {
	footer := formatMemoryContextFooter(&memory.ContextPack{
		Sources: []memory.ContextPackSource{
			{Name: "MEMORY.md", Path: "MEMORY.md", Count: 1, MaxScore: 0.94},
			{Name: "2026-04-29.md", Path: "memory/2026-04-29.md", Count: 2, MaxScore: 0.81},
		},
		Truncation: memory.ContextPackTruncation{Truncated: true, UsedChars: 900, BudgetChars: 1000},
	})

	for _, want := range []string{"Memory sources:", "MEMORY.md", "memory/2026-04-29.md", "truncated"} {
		if !strings.Contains(footer, want) {
			t.Fatalf("expected %q in footer %q", want, footer)
		}
	}
}
