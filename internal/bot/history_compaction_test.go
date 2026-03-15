package bot

import (
	"testing"

	"ok-gobot/internal/storage"
)

func TestSummaryRootsSelectsDeepestNodes(t *testing.T) {
	t.Parallel()

	nodes := []storage.SessionSummaryNode{
		{NodeKey: "d0:0000", Depth: 0, Ordinal: 0, SourceStartMessageID: 1, SourceEndMessageID: 2},
		{NodeKey: "d2:0001", Depth: 2, Ordinal: 1, SourceStartMessageID: 6, SourceEndMessageID: 9},
		{NodeKey: "d1:0000", Depth: 1, Ordinal: 0, SourceStartMessageID: 1, SourceEndMessageID: 5},
		{NodeKey: "d2:0000", Depth: 2, Ordinal: 0, SourceStartMessageID: 1, SourceEndMessageID: 5},
	}

	roots := summaryRoots(nodes)
	if len(roots) != 2 {
		t.Fatalf("expected 2 roots, got %d", len(roots))
	}
	if roots[0].NodeKey != "d2:0000" || roots[1].NodeKey != "d2:0001" {
		t.Fatalf("roots out of order: %+v", roots)
	}
	if maxCoveredMessageID(roots) != 9 {
		t.Fatalf("maxCoveredMessageID = %d, want 9", maxCoveredMessageID(roots))
	}
}

func TestBuildCompactedHistoryPrependsSummaryRoots(t *testing.T) {
	t.Parallel()

	roots := []storage.SessionSummaryNode{
		{NodeKey: "d2:0000", Depth: 2, Ordinal: 0, Content: "old summary", SourceStartMessageID: 1, SourceEndMessageID: 8},
	}
	tail := []storage.SessionMessageV2{
		{ID: 9, Role: "user", Content: "fresh question"},
		{ID: 10, Role: "assistant", Content: "fresh answer"},
	}

	history := buildCompactedHistory(roots, tail)
	if len(history) != 3 {
		t.Fatalf("expected 3 history messages, got %d", len(history))
	}
	if history[0].Role != "assistant" || history[0].Content == "old summary" {
		t.Fatalf("expected formatted summary root, got %+v", history[0])
	}
	if history[1].Content != "fresh question" || history[2].Content != "fresh answer" {
		t.Fatalf("unexpected tail order: %+v", history)
	}
}
