package agent

import (
	"context"
	"fmt"
	"strings"

	"ok-gobot/internal/ai"
)

// Density levels for context tree nodes.
const (
	DensityD0 = 0 // Raw transcript span (no summary stored — references messages directly)
	DensityD1 = 1 // First-level summary of a D0 span
	DensityD2 = 2 // High-level summary aggregating multiple D1 nodes
)

// ContextNode represents a summary node in the context tree.
// Each node covers a span of transcript messages and optionally
// references child nodes at a lower density level.
type ContextNode struct {
	ID         int64  // Database primary key (0 for unsaved nodes)
	SessionKey string // Owning session
	Density    int    // DensityD0, DensityD1, or DensityD2
	Summary    string // AI-generated summary text (empty for D0)
	SpanStart  int64  // First session_messages_v2.id in the span
	SpanEnd    int64  // Last session_messages_v2.id in the span
	ParentID   int64  // Parent node id (0 = root)
	TokenCount int    // Estimated token count of the summary
	CreatedAt  string // ISO timestamp
}

// ContextTree holds an ordered set of context nodes for a session,
// providing a multi-resolution view of conversation history.
type ContextTree struct {
	SessionKey string
	Nodes      []ContextNode
}

// FormatForPrompt renders the tree into a text block suitable for
// injection into the system prompt or conversation history. It walks
// from highest density (D2) down to D1, skipping D0 spans that are
// already covered by a higher-density summary.
func (ct *ContextTree) FormatForPrompt() string {
	if len(ct.Nodes) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[Compacted context tree]\n\n")

	// Group by density, highest first.
	for _, d := range []int{DensityD2, DensityD1} {
		nodes := ct.nodesAtDensity(d)
		if len(nodes) == 0 {
			continue
		}
		label := "D1"
		if d == DensityD2 {
			label = "D2"
		}
		for _, n := range nodes {
			sb.WriteString(fmt.Sprintf("[%s summary | msgs %d–%d]\n", label, n.SpanStart, n.SpanEnd))
			sb.WriteString(n.Summary)
			sb.WriteString("\n\n")
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}

// nodesAtDensity returns nodes filtered by density level, ordered by SpanStart.
func (ct *ContextTree) nodesAtDensity(density int) []ContextNode {
	var out []ContextNode
	for _, n := range ct.Nodes {
		if n.Density == density {
			out = append(out, n)
		}
	}
	return out
}

// TreeCompactor extends the base Compactor with context-tree aware compaction.
type TreeCompactor struct {
	*Compactor
}

// NewTreeCompactor creates a tree-aware compactor wrapping an AI client.
func NewTreeCompactor(aiClient ai.Client, model string) *TreeCompactor {
	return &TreeCompactor{
		Compactor: NewCompactor(aiClient, model),
	}
}

// CompactToD1 summarises a span of raw messages into a D1 context node.
// The returned node has Density=1 and span boundaries set to the first
// and last message IDs provided.
func (tc *TreeCompactor) CompactToD1(ctx context.Context, sessionKey string, messages []SpanMessage) (*ContextNode, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("no messages to compact")
	}

	aiMsgs := spanMessagesToAI(messages)
	result, err := tc.Compact(ctx, aiMsgs)
	if err != nil {
		return nil, fmt.Errorf("D1 compaction: %w", err)
	}

	return &ContextNode{
		SessionKey: sessionKey,
		Density:    DensityD1,
		Summary:    result.Summary,
		SpanStart:  messages[0].ID,
		SpanEnd:    messages[len(messages)-1].ID,
		TokenCount: result.SummaryTokens,
	}, nil
}

// CompactToD2 summarises a set of D1 nodes into a single D2 context node.
// The resulting node spans from the earliest D1 span start to the latest
// D1 span end.
func (tc *TreeCompactor) CompactToD2(ctx context.Context, sessionKey string, d1Nodes []ContextNode) (*ContextNode, error) {
	if len(d1Nodes) == 0 {
		return nil, fmt.Errorf("no D1 nodes to compact")
	}

	// Build a synthetic conversation from D1 summaries.
	var msgs []ai.Message
	for _, n := range d1Nodes {
		msgs = append(msgs, ai.Message{
			Role:    "assistant",
			Content: fmt.Sprintf("[Summary of messages %d–%d]\n%s", n.SpanStart, n.SpanEnd, n.Summary),
		})
	}

	result, err := tc.Compact(ctx, msgs)
	if err != nil {
		return nil, fmt.Errorf("D2 compaction: %w", err)
	}

	spanStart := d1Nodes[0].SpanStart
	spanEnd := d1Nodes[len(d1Nodes)-1].SpanEnd
	for _, n := range d1Nodes {
		if n.SpanStart < spanStart {
			spanStart = n.SpanStart
		}
		if n.SpanEnd > spanEnd {
			spanEnd = n.SpanEnd
		}
	}

	return &ContextNode{
		SessionKey: sessionKey,
		Density:    DensityD2,
		Summary:    result.Summary,
		SpanStart:  spanStart,
		SpanEnd:    spanEnd,
		TokenCount: result.SummaryTokens,
	}, nil
}

// SpanMessage is a raw transcript message with its database ID, used as
// input to tree compaction.
type SpanMessage struct {
	ID      int64
	Role    string
	Content string
}

// spanMessagesToAI converts SpanMessages to ai.Message for the compactor.
func spanMessagesToAI(msgs []SpanMessage) []ai.Message {
	out := make([]ai.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		out = append(out, ai.Message{Role: m.Role, Content: m.Content})
	}
	return out
}

// TreeCompactionResult holds the outcome of a tree-aware compaction pass.
type TreeCompactionResult struct {
	NewNodes       []ContextNode // Nodes created during this compaction
	ArchivedMsgIDs []int64       // Message IDs now covered by D1 nodes
	OriginalTokens int
	SummaryTokens  int
	TokensSaved    int
}

// FormatNotification formats the tree compaction result for display.
func (r *TreeCompactionResult) FormatNotification() string {
	d1Count := 0
	d2Count := 0
	for _, n := range r.NewNodes {
		switch n.Density {
		case DensityD1:
			d1Count++
		case DensityD2:
			d2Count++
		}
	}

	parts := []string{fmt.Sprintf("🌳 Context tree compacted: %d → %d tokens (%d saved)",
		r.OriginalTokens, r.SummaryTokens, r.TokensSaved)}

	if d1Count > 0 {
		parts = append(parts, fmt.Sprintf("D1 nodes created: %d", d1Count))
	}
	if d2Count > 0 {
		parts = append(parts, fmt.Sprintf("D2 nodes created: %d", d2Count))
	}
	parts = append(parts, fmt.Sprintf("Archived messages: %d", len(r.ArchivedMsgIDs)))

	return strings.Join(parts, "\n")
}

// MinD1SpanMessages is the minimum number of messages required to form a D1 node.
const MinD1SpanMessages = 4

// MinD1NodesForD2 is the minimum number of D1 nodes required to form a D2 node.
const MinD1NodesForD2 = 3
