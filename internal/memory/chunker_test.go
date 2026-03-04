package memory

import (
	"strings"
	"testing"
)

func TestChunkMarkdownNoHeaders(t *testing.T) {
	input := "This is a note.\n\nIt has two paragraphs."

	chunks := ChunkMarkdown("MEMORY.md", input)

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	chunk := chunks[0]
	if chunk.SourceFile != "MEMORY.md" {
		t.Fatalf("unexpected source_file: %q", chunk.SourceFile)
	}
	if chunk.ChunkOrdinal != 0 {
		t.Fatalf("unexpected chunk_ordinal: %d", chunk.ChunkOrdinal)
	}
	if len(chunk.HeaderPath) != 0 {
		t.Fatalf("expected empty header_path, got %v", chunk.HeaderPath)
	}
	if chunk.Content != input {
		t.Fatalf("unexpected content:\n%q", chunk.Content)
	}
}

func TestChunkMarkdownNestedHeadersAndPaths(t *testing.T) {
	input := strings.Join([]string{
		"# Alpha",
		"alpha body",
		"",
		"## Beta",
		"beta body",
		"",
		"### Gamma",
		"gamma body",
		"",
		"## Delta",
		"delta body",
		"",
		"# Epsilon",
		"epsilon body",
	}, "\n")

	chunks := ChunkMarkdown("memory/2026-03-04.md", input)

	if len(chunks) != 5 {
		t.Fatalf("expected 5 chunks, got %d", len(chunks))
	}

	wantPaths := [][]string{
		{"Alpha"},
		{"Alpha", "Beta"},
		{"Alpha", "Beta", "Gamma"},
		{"Alpha", "Delta"},
		{"Epsilon"},
	}
	for i, chunk := range chunks {
		if chunk.ChunkOrdinal != i {
			t.Fatalf("chunk %d has wrong ordinal %d", i, chunk.ChunkOrdinal)
		}
		if chunk.SourceFile != "memory/2026-03-04.md" {
			t.Fatalf("chunk %d source_file mismatch: %q", i, chunk.SourceFile)
		}
		if !equalStrings(chunk.HeaderPath, wantPaths[i]) {
			t.Fatalf("chunk %d header_path mismatch: got %v want %v", i, chunk.HeaderPath, wantPaths[i])
		}
	}

	if !strings.Contains(chunks[0].Content, "## Beta") || !strings.Contains(chunks[0].Content, "## Delta") {
		t.Fatalf("top-level chunk should extend until next # header, got:\n%s", chunks[0].Content)
	}
	if strings.Contains(chunks[1].Content, "## Delta") {
		t.Fatalf("second-level chunk should stop before next ## header, got:\n%s", chunks[1].Content)
	}
}

func TestChunkMarkdownIncludesPreamble(t *testing.T) {
	input := strings.Join([]string{
		"Standalone intro line.",
		"",
		"# Header",
		"Body text.",
	}, "\n")

	chunks := ChunkMarkdown("MEMORY.md", input)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks (preamble + header), got %d", len(chunks))
	}
	if len(chunks[0].HeaderPath) != 0 {
		t.Fatalf("preamble header_path should be empty, got %v", chunks[0].HeaderPath)
	}
	if !strings.Contains(chunks[1].Content, "# Header") {
		t.Fatalf("header chunk should contain header line, got:\n%s", chunks[1].Content)
	}
}

func TestChunkMarkdownSplitsAtParagraphBoundaries(t *testing.T) {
	chunker := NewMarkdownChunkerWithConfig(ChunkerConfig{
		SoftTargetChars: 80,
		HardMaxChars:    120,
	})

	para1 := strings.Repeat("a", 45)
	para2 := strings.Repeat("b", 45)
	para3 := strings.Repeat("c", 20)
	input := "# Title\n\n" + para1 + "\n\n" + para2 + "\n\n" + para3

	chunks := chunker.Chunk("MEMORY.md", input)

	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for _, chunk := range chunks {
		if !strings.HasPrefix(chunk.Content, "# Title\n\n") {
			t.Fatalf("split chunk should preserve header prefix, got:\n%s", chunk.Content)
		}
		if len(chunk.Content) > 120 {
			t.Fatalf("chunk exceeds hard max chars: %d", len(chunk.Content))
		}
	}
	if !containsChunkWithText(chunks, para1) {
		t.Fatalf("paragraph 1 was not preserved in a chunk")
	}
	if !containsChunkWithText(chunks, para2) {
		t.Fatalf("paragraph 2 was not preserved in a chunk")
	}
}

func TestChunkMarkdownEnforcesHardLimitForLongParagraph(t *testing.T) {
	chunker := NewMarkdownChunkerWithConfig(ChunkerConfig{
		SoftTargetChars: 70,
		HardMaxChars:    90,
	})

	longParagraph := strings.Repeat("word ", 80)
	input := "# Header\n\n" + strings.TrimSpace(longParagraph)

	chunks := chunker.Chunk("MEMORY.md", input)
	if len(chunks) < 2 {
		t.Fatalf("expected long paragraph to split, got %d chunk(s)", len(chunks))
	}

	for _, chunk := range chunks {
		if len(chunk.Content) > 90 {
			t.Fatalf("chunk length %d exceeds hard max", len(chunk.Content))
		}
	}
}

func TestChunkMarkdownIgnoresHeadersInsideFencedCode(t *testing.T) {
	input := strings.Join([]string{
		"# Top",
		"text before code",
		"```",
		"## not a real header",
		"```",
		"",
		"## Real child",
		"child text",
	}, "\n")

	chunks := ChunkMarkdown("MEMORY.md", input)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if !equalStrings(chunks[1].HeaderPath, []string{"Top", "Real child"}) {
		t.Fatalf("unexpected path for child chunk: %v", chunks[1].HeaderPath)
	}
	if strings.Contains(chunks[0].Content, "not a real header\n\n") {
		t.Fatalf("fenced-code header should not produce a separate chunk")
	}
}

func TestSplitByHardLimit(t *testing.T) {
	input := "one two three four five six seven eight"
	parts := splitByHardLimit(input, 10)

	if len(parts) < 2 {
		t.Fatalf("expected text to split into multiple parts, got %d", len(parts))
	}
	for _, part := range parts {
		if len(part) > 10 {
			t.Fatalf("part exceeds hard limit: %q (%d)", part, len(part))
		}
	}
}

func TestNormalizeHeaderTitle(t *testing.T) {
	got := normalizeHeaderTitle("Title ### ")
	if got != "Title" {
		t.Fatalf("unexpected normalized title: %q", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsChunkWithText(chunks []MarkdownChunk, text string) bool {
	for _, chunk := range chunks {
		if strings.Contains(chunk.Content, text) {
			return true
		}
	}
	return false
}
