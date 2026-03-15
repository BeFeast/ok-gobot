package agent

import (
	"context"
	"fmt"
	"testing"

	"ok-gobot/internal/ai"
)

type compactorAIStub struct {
	responses []string
	calls     int
}

func (s *compactorAIStub) Complete(_ context.Context, _ []ai.Message) (string, error) {
	if s.calls >= len(s.responses) {
		return "", fmt.Errorf("unexpected call %d", s.calls)
	}
	resp := s.responses[s.calls]
	s.calls++
	return resp, nil
}

func (s *compactorAIStub) CompleteWithTools(_ context.Context, _ []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	return nil, fmt.Errorf("CompleteWithTools should not be called")
}

func TestCompactorCompactTranscriptBuildsLinkedTree(t *testing.T) {
	t.Parallel()

	stub := &compactorAIStub{
		responses: []string{
			"d0-0", "d0-1", "d0-2", "d0-3",
			"d1-0", "d1-1",
			"d2-0",
		},
	}
	compactor := NewCompactor(stub, "gpt-4o")
	compactor.treeConfig = compactionTreeConfig{
		keepTailMessages:   2,
		d0ChunkTargetToken: 5,
		fanout:             2,
		maxDepth:           2,
	}

	transcript := []TranscriptMessage{
		{ID: 1, Role: "user", Content: "one"},
		{ID: 2, Role: "assistant", Content: "two"},
		{ID: 3, Role: "user", Content: "three"},
		{ID: 4, Role: "assistant", Content: "four"},
		{ID: 5, Role: "user", Content: "five"},
		{ID: 6, Role: "assistant", Content: "six"},
	}

	result, err := compactor.CompactTranscript(context.Background(), transcript)
	if err != nil {
		t.Fatalf("CompactTranscript: %v", err)
	}

	if result.D0Count != 4 || result.D1Count != 2 || result.D2Count != 1 {
		t.Fatalf("unexpected depth counts: D0=%d D1=%d D2=%d", result.D0Count, result.D1Count, result.D2Count)
	}
	if result.CoveredUntilMessageID != 4 {
		t.Fatalf("CoveredUntilMessageID = %d, want 4", result.CoveredUntilMessageID)
	}
	if len(result.TailMessages) != 2 || result.TailMessages[0].ID != 5 || result.TailMessages[1].ID != 6 {
		t.Fatalf("unexpected tail: %+v", result.TailMessages)
	}
	if len(result.RootNodes) != 1 {
		t.Fatalf("expected one root node, got %d", len(result.RootNodes))
	}

	root := result.RootNodes[0]
	if root.Depth != 2 || root.Key != "d2:0000" {
		t.Fatalf("unexpected root node: %+v", root)
	}
	if root.SourceStartID != 1 || root.SourceEndID != 4 {
		t.Fatalf("unexpected root source span: %+v", root)
	}
	if root.ChildStartKey != "d1:0000" || root.ChildEndKey != "d1:0001" {
		t.Fatalf("unexpected root child span: %+v", root)
	}
	if result.Summary != "d2-0" {
		t.Fatalf("Summary = %q, want d2-0", result.Summary)
	}
	if stub.calls != 7 {
		t.Fatalf("expected 7 AI calls, got %d", stub.calls)
	}
}

func TestCompactorCompactUsesSyntheticIDsForLegacyMessages(t *testing.T) {
	t.Parallel()

	stub := &compactorAIStub{
		responses: []string{
			"d0-0", "d0-1",
			"d1-0",
			"d2-0",
		},
	}
	compactor := NewCompactor(stub, "gpt-4o")
	compactor.treeConfig = compactionTreeConfig{
		keepTailMessages:   1,
		d0ChunkTargetToken: 5,
		fanout:             2,
		maxDepth:           2,
	}

	result, err := compactor.Compact(context.Background(), []ai.Message{
		{Role: "system", Content: "ignore"},
		{Role: "user", Content: "one"},
		{Role: "assistant", Content: "two"},
		{Role: "user", Content: "three"},
	})
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}

	if result.CoveredUntilMessageID != 2 {
		t.Fatalf("CoveredUntilMessageID = %d, want 2", result.CoveredUntilMessageID)
	}
	if len(result.TailMessages) != 1 || result.TailMessages[0].ID != 3 {
		t.Fatalf("unexpected tail: %+v", result.TailMessages)
	}
}
