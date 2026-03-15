package bot

import (
	"strings"
	"testing"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
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

func TestTrimCompactedHistoryToTokenBudgetPreservesSummaryRoots(t *testing.T) {
	t.Parallel()

	roots := []storage.SessionSummaryNode{
		{NodeKey: "d2:0000", Depth: 2, Ordinal: 0, Content: "dense summary", SourceStartMessageID: 1, SourceEndMessageID: 8},
	}
	tail := []storage.SessionMessageV2{
		{ID: 9, Role: ai.RoleUser, Content: strings.Repeat("u", 120000)},
		{ID: 10, Role: ai.RoleAssistant, Content: strings.Repeat("a", 120000)},
	}

	history := trimCompactedHistoryToTokenBudget(roots, tail, "gpt-4")
	if len(history) == 0 {
		t.Fatal("expected compacted history to retain summary roots")
	}
	if !strings.HasPrefix(history[0].Content, "[Compacted context D2; transcript 1-8]") {
		t.Fatalf("expected first message to be preserved summary root, got %+v", history[0])
	}
	if got, budget := countChatHistoryTokens(history), int(float64(agent.ModelLimits("gpt-4"))*0.40); got > budget {
		t.Fatalf("trimmed compacted history tokens = %d, want <= %d", got, budget)
	}
}
