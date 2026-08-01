package bootstrap

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/tools"
)

// fixtureLoader builds a loader populated with all canonical bootstrap files
// plus today's, yesterday's, and an older daily note. The clock is frozen at
// 2026-03-03 so tests can assert exact date strings.
func fixtureLoader(t *testing.T) (*Loader, time.Time) {
	t.Helper()

	frozen := time.Date(2026, time.March, 3, 12, 0, 0, 0, time.UTC)
	basePath := t.TempDir()

	writeTestFile(t, filepath.Join(basePath, "IDENTITY.md"), "# Identity\nName: TestBot\nEmoji: 🤖")
	writeTestFile(t, filepath.Join(basePath, "SOUL.md"), "Soul line")
	writeTestFile(t, filepath.Join(basePath, "USER.md"), "User line")
	writeTestFile(t, filepath.Join(basePath, "AGENTS.md"), "Agents line")
	writeTestFile(t, filepath.Join(basePath, "TOOLS.md"), "Tools line")
	writeTestFile(t, filepath.Join(basePath, "HEARTBEAT.md"), "Heartbeat line")
	writeTestFile(t, filepath.Join(basePath, "MEMORY.md"), "Memory line")
	writeTestFile(t, filepath.Join(basePath, "memory", "2026-03-03.md"), "Today line")
	writeTestFile(t, filepath.Join(basePath, "memory", "2026-03-02.md"), "Yesterday line")
	writeTestFile(t, filepath.Join(basePath, "memory", "2026-02-15.md"), "Older line")

	loader, err := newLoader(basePath, func() time.Time { return frozen })
	if err != nil {
		t.Fatalf("newLoader() error = %v", err)
	}
	return loader, frozen
}

func TestSystemPromptForMode_Eager_InlinesTodayAndYesterday(t *testing.T) {
	loader, _ := fixtureLoader(t)
	got := loader.SystemPromptForMode(MemoryModeEager)

	if !strings.Contains(got, "Today line") {
		t.Errorf("eager mode should inline today's daily note")
	}
	if !strings.Contains(got, "Yesterday line") {
		t.Errorf("eager mode should inline yesterday's daily note")
	}
	if !strings.Contains(got, "Memory line") {
		t.Errorf("eager mode should inline MEMORY.md")
	}
	if strings.Contains(got, "Older line") {
		t.Errorf("eager mode must not inline notes older than yesterday")
	}
}

func TestSystemPromptForMode_RetrievalFirst_OmitsAllDailyNotes(t *testing.T) {
	loader, _ := fixtureLoader(t)
	got := loader.SystemPromptForMode(MemoryModeRetrievalFirst)

	if !strings.Contains(got, "Memory line") {
		t.Errorf("retrieval_first must keep MEMORY.md so identity context survives")
	}
	for _, banned := range []string{"Today line", "Yesterday line", "Older line", "## DAILY MEMORY:"} {
		if strings.Contains(got, banned) {
			t.Errorf("retrieval_first prompt unexpectedly contains %q", banned)
		}
	}
}

func TestSystemPromptForMode_StartupRecent_InlinesOnlyToday(t *testing.T) {
	loader, _ := fixtureLoader(t)
	got := loader.SystemPromptForMode(MemoryModeStartupRecent)

	if !strings.Contains(got, "Memory line") {
		t.Errorf("startup_recent must keep MEMORY.md inlined")
	}
	if !strings.Contains(got, "Today line") {
		t.Errorf("startup_recent must inline today's daily note")
	}
	if strings.Contains(got, "Yesterday line") {
		t.Errorf("startup_recent must NOT inline yesterday's note (retrieval-only)")
	}
	if strings.Contains(got, "Older line") {
		t.Errorf("startup_recent must NOT inline older notes")
	}
}

func TestDailyNoteSourcesForMode_ReportsRetrievalOnlyNotes(t *testing.T) {
	// Loader only tracks today + yesterday in Files (older notes live on disk
	// but are reached via the memory index, not the bootstrap loader).
	loader, _ := fixtureLoader(t)

	cases := []struct {
		mode string
		want []string
	}{
		{MemoryModeEager, nil},
		{MemoryModeRetrievalFirst, []string{"memory/2026-03-02.md", "memory/2026-03-03.md"}},
		{MemoryModeStartupRecent, []string{"memory/2026-03-02.md"}},
	}

	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			got := loader.DailyNoteSourcesForMode(tc.mode)
			if len(got) != len(tc.want) {
				t.Fatalf("mode=%s: got %v, want %v", tc.mode, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("mode=%s: got[%d]=%s want %s", tc.mode, i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestDailyNoteDatesForMode_OnlyReturnsExistingDates(t *testing.T) {
	loader, _ := fixtureLoader(t)

	if got := loader.DailyNoteDatesForMode(MemoryModeRetrievalFirst); len(got) != 0 {
		t.Errorf("retrieval_first must inline no daily notes, got %v", got)
	}
	if got := loader.DailyNoteDatesForMode(MemoryModeStartupRecent); len(got) != 1 || got[0] != "2026-03-03" {
		t.Errorf("startup_recent dates = %v, want [2026-03-03]", got)
	}
	got := loader.DailyNoteDatesForMode(MemoryModeEager)
	if len(got) != 2 || got[0] != "2026-03-03" || got[1] != "2026-03-02" {
		t.Errorf("eager dates = %v, want [2026-03-03 2026-03-02]", got)
	}
}

func TestDailyNoteDatesForMode_MissingFilesAreSkipped(t *testing.T) {
	frozen := time.Date(2026, time.March, 3, 12, 0, 0, 0, time.UTC)
	basePath := t.TempDir()
	writeTestFile(t, filepath.Join(basePath, "IDENTITY.md"), "Name: T")
	writeTestFile(t, filepath.Join(basePath, "memory", "2026-03-02.md"), "Y")

	loader, err := newLoader(basePath, func() time.Time { return frozen })
	if err != nil {
		t.Fatalf("newLoader() error = %v", err)
	}

	got := loader.DailyNoteDatesForMode(MemoryModeEager)
	if len(got) != 1 || got[0] != "2026-03-02" {
		t.Errorf("eager dates with only yesterday file = %v, want [2026-03-02]", got)
	}
}

func TestNormalizeMemoryMode(t *testing.T) {
	cases := map[string]string{
		"":                     MemoryModeEager,
		"eager":                MemoryModeEager,
		"EAGER":                MemoryModeEager,
		"  retrieval_first  ":  MemoryModeRetrievalFirst,
		"startup_recent":       MemoryModeStartupRecent,
		"unknown-future-value": MemoryModeEager,
	}
	for in, want := range cases {
		if got := NormalizeMemoryMode(in); got != want {
			t.Errorf("NormalizeMemoryMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildPrompt_RetrievalFirstMemorySection(t *testing.T) {
	loader, _ := fixtureLoader(t)
	registry := tools.NewRegistry()
	registry.Register(testTool{name: "memory_search", desc: "search"})
	registry.Register(testTool{name: "memory_get", desc: "get"})

	got := BuildPrompt(loader, registry, PromptOptions{
		Mode:       "full",
		MemoryMode: MemoryModeRetrievalFirst,
		Now: func() time.Time {
			return time.Date(2026, time.March, 3, 9, 0, 0, 0, time.UTC)
		},
	})

	for _, banned := range []string{"Today line", "Yesterday line", "Older line"} {
		if strings.Contains(got, banned) {
			t.Errorf("retrieval_first prompt unexpectedly contained %q", banned)
		}
	}
	for _, want := range []string{
		"Memory mode: retrieval_first.",
		"Daily notes (memory/YYYY-MM-DD.md) are NOT inlined",
		"call memory_get with the exact source + header_path",
		"Cite source paths",
		"Available retrieval-only daily notes:",
		"memory/2026-03-02.md",
		"memory/2026-03-03.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("retrieval_first prompt missing expected fragment %q", want)
		}
	}
}

func TestBuildPrompt_StartupRecentMemorySection(t *testing.T) {
	loader, _ := fixtureLoader(t)
	registry := tools.NewRegistry()
	registry.Register(testTool{name: "memory_search", desc: "search"})

	got := BuildPrompt(loader, registry, PromptOptions{
		Mode:       "full",
		MemoryMode: MemoryModeStartupRecent,
		Now: func() time.Time {
			return time.Date(2026, time.March, 3, 9, 0, 0, 0, time.UTC)
		},
	})

	if !strings.Contains(got, "Today line") {
		t.Errorf("startup_recent must inline today's daily note")
	}
	if strings.Contains(got, "Yesterday line") {
		t.Errorf("startup_recent must NOT inline yesterday's daily note")
	}
	for _, want := range []string{
		"Memory mode: startup_recent.",
		"Today's daily note is inlined above",
		"Available retrieval-only daily notes:",
		"memory/2026-03-02.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("startup_recent prompt missing expected fragment %q", want)
		}
	}
}

func TestBuildPrompt_DailyNotesNotDuplicatedInBothPromptAndRetrievalHint(t *testing.T) {
	loader, _ := fixtureLoader(t)
	registry := tools.NewRegistry()
	registry.Register(testTool{name: "memory_search", desc: "search"})

	cases := []string{MemoryModeEager, MemoryModeRetrievalFirst, MemoryModeStartupRecent}
	for _, mode := range cases {
		t.Run(mode, func(t *testing.T) {
			got := BuildPrompt(loader, registry, PromptOptions{
				Mode:       "full",
				MemoryMode: mode,
				Now: func() time.Time {
					return time.Date(2026, time.March, 3, 9, 0, 0, 0, time.UTC)
				},
			})

			inlined := loader.DailyNoteDatesForMode(mode)
			retrievalOnly := loader.DailyNoteSourcesForMode(mode)
			for _, date := range inlined {
				key := "memory/" + date + ".md"
				for _, src := range retrievalOnly {
					if src == key {
						t.Fatalf("mode %s: %s appears in BOTH inlined and retrieval-only sets", mode, key)
					}
				}
			}

			// Sanity: prompt only carries each daily note's content once.
			if mode == MemoryModeEager {
				if c := strings.Count(got, "Today line"); c != 1 {
					t.Errorf("eager: 'Today line' appears %d times, want 1", c)
				}
				if c := strings.Count(got, "Yesterday line"); c != 1 {
					t.Errorf("eager: 'Yesterday line' appears %d times, want 1", c)
				}
			}
		})
	}
}

func TestBuildPrompt_EagerStillEmitsLegacyMemoryGuidance(t *testing.T) {
	loader, _ := fixtureLoader(t)
	registry := tools.NewRegistry()
	registry.Register(testTool{name: "memory_search", desc: "search"})

	got := BuildPrompt(loader, registry, PromptOptions{
		Mode:       "full",
		MemoryMode: "", // empty -> eager
		Now: func() time.Time {
			return time.Date(2026, time.March, 3, 9, 0, 0, 0, time.UTC)
		},
	})

	if !strings.Contains(got, "Memory mode: eager.") {
		t.Errorf("default mode should emit eager memory section")
	}
	if !strings.Contains(got, "Today line") || !strings.Contains(got, "Yesterday line") {
		t.Errorf("eager prompt must inline both today and yesterday for backward compatibility")
	}
}
