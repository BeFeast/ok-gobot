package agent

import (
	"context"
	"testing"

	"ok-gobot/internal/ai"
)

// stubClient implements ai.Client, returning a fixed summary string.
type stubClient struct{ summary string }

func (s *stubClient) Complete(_ context.Context, _ []ai.Message) (string, error) {
	return s.summary, nil
}

func (s *stubClient) CompleteWithTools(_ context.Context, _ []ai.ChatMessage, _ []ai.ToolDefinition) (*ai.ChatCompletionResponse, error) {
	return &ai.ChatCompletionResponse{}, nil
}

func TestCompactToD1(t *testing.T) {
	tc := NewTreeCompactor(&stubClient{summary: "D1 summary"}, "gpt-4o")

	msgs := []SpanMessage{
		{ID: 10, Role: "user", Content: "hello"},
		{ID: 11, Role: "assistant", Content: "hi"},
		{ID: 12, Role: "user", Content: "how are you"},
		{ID: 13, Role: "assistant", Content: "fine thanks"},
	}

	node, err := tc.CompactToD1(context.Background(), "test-session", msgs)
	if err != nil {
		t.Fatalf("CompactToD1: %v", err)
	}

	if node.Density != DensityD1 {
		t.Errorf("Density = %d, want %d", node.Density, DensityD1)
	}
	if node.Summary != "D1 summary" {
		t.Errorf("Summary = %q, want %q", node.Summary, "D1 summary")
	}
	if node.SpanStart != 10 {
		t.Errorf("SpanStart = %d, want 10", node.SpanStart)
	}
	if node.SpanEnd != 13 {
		t.Errorf("SpanEnd = %d, want 13", node.SpanEnd)
	}
	if node.SessionKey != "test-session" {
		t.Errorf("SessionKey = %q, want %q", node.SessionKey, "test-session")
	}
}

func TestCompactToD1_EmptyMessages(t *testing.T) {
	tc := NewTreeCompactor(&stubClient{summary: "whatever"}, "gpt-4o")

	_, err := tc.CompactToD1(context.Background(), "test", nil)
	if err == nil {
		t.Fatal("expected error for empty messages")
	}
}

func TestCompactToD2(t *testing.T) {
	tc := NewTreeCompactor(&stubClient{summary: "D2 high-level summary"}, "gpt-4o")

	d1Nodes := []ContextNode{
		{ID: 1, Density: DensityD1, Summary: "first chunk summary", SpanStart: 1, SpanEnd: 10},
		{ID: 2, Density: DensityD1, Summary: "second chunk summary", SpanStart: 11, SpanEnd: 20},
		{ID: 3, Density: DensityD1, Summary: "third chunk summary", SpanStart: 21, SpanEnd: 30},
	}

	node, err := tc.CompactToD2(context.Background(), "test-session", d1Nodes)
	if err != nil {
		t.Fatalf("CompactToD2: %v", err)
	}

	if node.Density != DensityD2 {
		t.Errorf("Density = %d, want %d", node.Density, DensityD2)
	}
	if node.Summary != "D2 high-level summary" {
		t.Errorf("Summary = %q, want %q", node.Summary, "D2 high-level summary")
	}
	if node.SpanStart != 1 {
		t.Errorf("SpanStart = %d, want 1", node.SpanStart)
	}
	if node.SpanEnd != 30 {
		t.Errorf("SpanEnd = %d, want 30", node.SpanEnd)
	}
}

func TestCompactToD2_EmptyNodes(t *testing.T) {
	tc := NewTreeCompactor(&stubClient{summary: "whatever"}, "gpt-4o")

	_, err := tc.CompactToD2(context.Background(), "test", nil)
	if err == nil {
		t.Fatal("expected error for empty D1 nodes")
	}
}

func TestContextTreeFormatForPrompt(t *testing.T) {
	tree := &ContextTree{
		SessionKey: "test",
		Nodes: []ContextNode{
			{Density: DensityD2, Summary: "high level overview", SpanStart: 1, SpanEnd: 30},
			{Density: DensityD1, Summary: "first conversation", SpanStart: 1, SpanEnd: 10},
			{Density: DensityD1, Summary: "second conversation", SpanStart: 11, SpanEnd: 20},
		},
	}

	output := tree.FormatForPrompt()
	if output == "" {
		t.Fatal("expected non-empty output")
	}

	// Should contain the tree header
	if !contains(output, "[Compacted context tree]") {
		t.Error("missing tree header")
	}
	// Should contain D2 label
	if !contains(output, "[D2 summary") {
		t.Error("missing D2 label")
	}
	// Should contain D1 labels
	if !contains(output, "[D1 summary") {
		t.Error("missing D1 label")
	}
	// Should contain the summaries
	if !contains(output, "high level overview") {
		t.Error("missing D2 summary content")
	}
}

func TestContextTreeFormatForPrompt_Empty(t *testing.T) {
	tree := &ContextTree{SessionKey: "test"}
	if tree.FormatForPrompt() != "" {
		t.Error("expected empty output for empty tree")
	}
}

func TestTreeCompactionResultFormatNotification(t *testing.T) {
	result := &TreeCompactionResult{
		NewNodes: []ContextNode{
			{Density: DensityD1},
			{Density: DensityD2},
		},
		ArchivedMsgIDs: []int64{1, 2, 3, 4, 5},
		OriginalTokens: 1000,
		SummaryTokens:  200,
		TokensSaved:    800,
	}

	output := result.FormatNotification()
	if !contains(output, "1000") || !contains(output, "200") || !contains(output, "800") {
		t.Errorf("notification missing token counts: %s", output)
	}
	if !contains(output, "D1 nodes created: 1") {
		t.Error("missing D1 count")
	}
	if !contains(output, "D2 nodes created: 1") {
		t.Error("missing D2 count")
	}
	if !contains(output, "Archived messages: 5") {
		t.Error("missing archived count")
	}
}

func TestSpanMessagesToAI_SkipsSystem(t *testing.T) {
	msgs := []SpanMessage{
		{ID: 1, Role: "system", Content: "you are a bot"},
		{ID: 2, Role: "user", Content: "hello"},
		{ID: 3, Role: "assistant", Content: "hi"},
	}
	aiMsgs := spanMessagesToAI(msgs)
	if len(aiMsgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(aiMsgs))
	}
	if aiMsgs[0].Role != "user" {
		t.Errorf("first message role = %q, want user", aiMsgs[0].Role)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
