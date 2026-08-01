package memory

import (
	"context"
	"strings"
	"testing"
)

func TestBuildContextPackRanksAndCitesResults(t *testing.T) {
	pack := BuildContextPackFromResults(ContextPackRequest{
		Query:  "memory context builder",
		Scope:  ContextPackScope{SessionKey: "dm:42", ChatID: 42},
		Budget: ContextPackBudget{MaxChars: 2000, MaxItems: 3, SnippetChars: 200},
	}, []MemoryResult{
		{
			Source:       "memory/2026-04-29.md",
			SourceFile:   "memory/2026-04-29.md",
			HeaderPath:   "Daily",
			ChunkOrdinal: 2,
			Content:      "Lower scoring daily note about unrelated follow-up.",
			Similarity:   0.61,
		},
		{
			Source:     "MEMORY.md",
			SourceFile: "MEMORY.md",
			HeaderPath: "Projects > OK Gobot",
			StartLine:  12,
			EndLine:    15,
			Content:    "Use a bounded memory context pack with citations and scores for runtime prompts.",
			Similarity: 0.95,
		},
	})

	if len(pack.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(pack.Items))
	}
	first := pack.Items[0]
	if first.Citation.SourcePath != "MEMORY.md" {
		t.Fatalf("expected highest score first from MEMORY.md, got %+v", first.Citation)
	}
	if first.Citation.Locator != "lines 12-15" {
		t.Fatalf("expected line citation, got %q", first.Citation.Locator)
	}
	for _, want := range []string{"Source: MEMORY.md", "Header: Projects > OK Gobot", "score: 0.95", "lines 12-15"} {
		if !strings.Contains(pack.Text, want) {
			t.Fatalf("expected %q in rendered pack:\n%s", want, pack.Text)
		}
	}
}

func TestBuildContextPackDeduplicatesNearIdenticalChunks(t *testing.T) {
	pack := BuildContextPackFromResults(ContextPackRequest{
		Query:  "deploy restart plan",
		Budget: ContextPackBudget{MaxChars: 2000, MaxItems: 5, SnippetChars: 200},
	}, []MemoryResult{
		{
			SourceFile:   "MEMORY.md",
			ChunkOrdinal: 1,
			Content:      "Deploy plan: restart ok-gobot after running the migration and checking logs.",
			Similarity:   0.91,
		},
		{
			SourceFile:   "extra/project.md",
			ChunkOrdinal: 7,
			Content:      "deploy plan restart ok gobot after running migration checking logs",
			Similarity:   0.88,
		},
		{
			SourceFile:   "memory/2026-04-29.md",
			ChunkOrdinal: 3,
			Content:      "Remember to update the Telegram operator with the memory sources used.",
			Similarity:   0.72,
		},
	})

	if len(pack.Items) != 2 {
		t.Fatalf("expected 2 deduped items, got %d", len(pack.Items))
	}
	if pack.Truncation.DedupeDropped != 1 {
		t.Fatalf("expected 1 deduped result, got %d", pack.Truncation.DedupeDropped)
	}
	if strings.Contains(pack.Text, "extra/project.md") {
		t.Fatalf("near-duplicate lower-ranked source should have been dropped:\n%s", pack.Text)
	}
}

func TestBuildContextPackEnforcesHardBudget(t *testing.T) {
	longText := strings.Repeat("memory budget citations need hard trimming ", 40)
	pack := BuildContextPackFromResults(ContextPackRequest{
		Query:  "budget",
		Budget: ContextPackBudget{MaxChars: 260, MaxItems: 3, SnippetChars: 1000},
	}, []MemoryResult{
		{SourceFile: "MEMORY.md", ChunkOrdinal: 1, Content: longText, Similarity: 0.99},
		{SourceFile: "memory/2026-04-29.md", ChunkOrdinal: 2, Content: "second item should be omitted by budget", Similarity: 0.80},
	})

	if len(pack.Text) > 260 {
		t.Fatalf("pack exceeded hard budget: len=%d text=%q", len(pack.Text), pack.Text)
	}
	if !pack.Truncation.Truncated {
		t.Fatalf("expected truncation metadata, got %+v", pack.Truncation)
	}
	if pack.Truncation.UsedChars != len(pack.Text) {
		t.Fatalf("used chars = %d, want %d", pack.Truncation.UsedChars, len(pack.Text))
	}
}

func TestBuildContextPackEmptyResults(t *testing.T) {
	pack := BuildContextPackFromResults(ContextPackRequest{
		Query:  "nothing here",
		Budget: ContextPackBudget{MaxChars: 500},
	}, nil)

	if pack.HasContent() {
		t.Fatal("empty results should not produce content items")
	}
	if pack.SourceSummary() != "none" {
		t.Fatalf("expected no source summary, got %q", pack.SourceSummary())
	}
	if !strings.Contains(pack.Text, "No relevant memory found") {
		t.Fatalf("expected empty result explanation, got:\n%s", pack.Text)
	}
}

func TestContextPackBuilderCombinesSearcherAndPrefetchedResults(t *testing.T) {
	searcher := &fakeContextPackSearcher{
		results: []MemoryResult{{SourceFile: "MEMORY.md", Content: "searched result", Similarity: 0.80}},
	}
	builder := NewContextPackBuilder(searcher)

	pack, err := builder.Build(context.Background(), ContextPackRequest{
		Query:   "combine",
		Budget:  ContextPackBudget{MaxChars: 1000, SearchTopK: 9},
		Results: []MemoryResult{{SourceFile: "qmd://optional", Content: "prefetched qmd result", Similarity: 0.90}},
	})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if len(pack.Items) != 2 {
		t.Fatalf("expected 2 combined items, got %d", len(pack.Items))
	}
	if pack.Items[0].Citation.SourcePath != "qmd://optional" {
		t.Fatalf("expected prefetched higher score first, got %+v", pack.Items[0].Citation)
	}
	if searcher.topK != 9 {
		t.Fatalf("search topK = %d, want 9", searcher.topK)
	}
}

type fakeContextPackSearcher struct {
	results []MemoryResult
	topK    int
}

func (f *fakeContextPackSearcher) Search(_ context.Context, _ string, topK int) ([]MemoryResult, error) {
	f.topK = topK
	return f.results, nil
}
