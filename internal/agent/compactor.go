package agent

import (
	"context"
	"fmt"
	"strings"

	"ok-gobot/internal/ai"
)

const (
	defaultKeepTailMessages   = 8
	defaultD0ChunkTargetToken = 1200
	defaultSummaryFanout      = 4
	maxSummaryDepth           = 2
)

type compactionTreeConfig struct {
	keepTailMessages   int
	d0ChunkTargetToken int
	fanout             int
	maxDepth           int
}

// TranscriptMessage is a raw transcript entry with a stable source ID.
type TranscriptMessage struct {
	ID      int64
	Role    string
	Content string
}

// SummaryNode is one D0/D1/D2 summary node linked back to transcript spans.
type SummaryNode struct {
	Key           string
	Depth         int
	Ordinal       int
	Content       string
	SourceStartID int64
	SourceEndID   int64
	ChildStartKey string
	ChildEndKey   string
}

// Compactor handles context compaction by summarizing old messages.
type Compactor struct {
	aiClient     ai.Client
	tokenCounter *TokenCounter
	threshold    float64 // Percentage of context to trigger compaction (0.8 = 80%)
	model        string
	treeConfig   compactionTreeConfig
}

// NewCompactor creates a new context compactor.
func NewCompactor(aiClient ai.Client, model string) *Compactor {
	return &Compactor{
		aiClient:     aiClient,
		tokenCounter: NewTokenCounter(),
		threshold:    0.8, // Compact at 80% of context limit
		model:        model,
		treeConfig: compactionTreeConfig{
			keepTailMessages:   defaultKeepTailMessages,
			d0ChunkTargetToken: defaultD0ChunkTargetToken,
			fanout:             defaultSummaryFanout,
			maxDepth:           maxSummaryDepth,
		},
	}
}

// SetThreshold sets the compaction threshold (0.0 to 1.0).
func (c *Compactor) SetThreshold(threshold float64) {
	if threshold > 0 && threshold <= 1.0 {
		c.threshold = threshold
	}
}

// ShouldCompact determines if context should be compacted.
func (c *Compactor) ShouldCompact(messages []ai.Message) bool {
	msgs := make([]Message, len(messages))
	for i, m := range messages {
		msgs[i] = Message{Role: m.Role, Content: m.Content}
	}
	return c.tokenCounter.ShouldCompact(msgs, c.model, c.threshold)
}

// ShouldCompactByTokens determines if context should be compacted based on token count.
func (c *Compactor) ShouldCompactByTokens(currentTokens int) bool {
	limit := ModelLimits(c.model)
	maxTokens := int(float64(limit) * c.threshold)
	return currentTokens > maxTokens
}

// Compact builds a D0/D1/D2 summary tree over the older part of the conversation.
func (c *Compactor) Compact(ctx context.Context, messages []ai.Message) (*CompactionResult, error) {
	transcript := make([]TranscriptMessage, 0, len(messages))
	nextID := int64(1)
	for _, msg := range messages {
		if msg.Role == "system" {
			continue
		}
		transcript = append(transcript, TranscriptMessage{
			ID:      nextID,
			Role:    msg.Role,
			Content: msg.Content,
		})
		nextID++
	}
	return c.CompactTranscript(ctx, transcript)
}

// CompactTranscript builds a source-linked context tree over the older transcript
// while leaving a fresh raw tail uncompressed.
func (c *Compactor) CompactTranscript(ctx context.Context, transcript []TranscriptMessage) (*CompactionResult, error) {
	if c.aiClient == nil {
		return nil, fmt.Errorf("AI client not configured")
	}

	filtered := make([]TranscriptMessage, 0, len(transcript))
	for _, msg := range transcript {
		if msg.Role == "system" || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		filtered = append(filtered, msg)
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no transcript messages to compact")
	}

	tailCount := c.treeConfig.keepTailMessages
	if tailCount >= len(filtered) {
		tailCount = len(filtered) - 1
	}
	if tailCount < 0 {
		tailCount = 0
	}
	if len(filtered)-tailCount < 1 {
		return nil, fmt.Errorf("not enough transcript history to compact")
	}

	cold := filtered[:len(filtered)-tailCount]
	tail := filtered[len(filtered)-tailCount:]

	d0Chunks := c.chunkTranscript(cold)
	if len(d0Chunks) == 0 {
		return nil, fmt.Errorf("not enough transcript history to compact")
	}

	nodes := make([]SummaryNode, 0, len(d0Chunks))
	currentLevel := make([]SummaryNode, 0, len(d0Chunks))

	for ordinal, chunk := range d0Chunks {
		summary, err := c.summarizeTranscriptChunk(ctx, chunk)
		if err != nil {
			return nil, err
		}
		node := SummaryNode{
			Key:           formatNodeKey(0, ordinal),
			Depth:         0,
			Ordinal:       ordinal,
			Content:       summary,
			SourceStartID: chunk[0].ID,
			SourceEndID:   chunk[len(chunk)-1].ID,
		}
		nodes = append(nodes, node)
		currentLevel = append(currentLevel, node)
	}

	depthCounts := [3]int{len(currentLevel), 0, 0}
	for depth := 1; depth <= c.treeConfig.maxDepth; depth++ {
		groups := groupSummaryNodes(currentLevel, c.treeConfig.fanout)
		nextLevel := make([]SummaryNode, 0, len(groups))
		for ordinal, group := range groups {
			summary, err := c.summarizeSummaryGroup(ctx, depth, group)
			if err != nil {
				return nil, err
			}
			node := SummaryNode{
				Key:           formatNodeKey(depth, ordinal),
				Depth:         depth,
				Ordinal:       ordinal,
				Content:       summary,
				SourceStartID: group[0].SourceStartID,
				SourceEndID:   group[len(group)-1].SourceEndID,
				ChildStartKey: group[0].Key,
				ChildEndKey:   group[len(group)-1].Key,
			}
			nodes = append(nodes, node)
			nextLevel = append(nextLevel, node)
		}
		currentLevel = nextLevel
		if depth < len(depthCounts) {
			depthCounts[depth] = len(currentLevel)
		}
	}

	rootSummaries := make([]string, 0, len(currentLevel))
	rootNodes := append([]SummaryNode(nil), currentLevel...)
	for _, node := range rootNodes {
		rootSummaries = append(rootSummaries, node.Content)
	}
	rootSummaryText := strings.Join(rootSummaries, "\n\n")

	originalTokens := c.countTranscriptTokens(filtered)
	compactedTokens := c.countSummaryContextTokens(rootNodes, tail)

	return &CompactionResult{
		Summary:               rootSummaryText,
		OriginalTokens:        originalTokens,
		SummaryTokens:         compactedTokens,
		TokensSaved:           originalTokens - compactedTokens,
		Nodes:                 nodes,
		RootNodes:             rootNodes,
		TailMessages:          append([]TranscriptMessage(nil), tail...),
		CoveredUntilMessageID: cold[len(cold)-1].ID,
		D0Count:               depthCounts[0],
		D1Count:               depthCounts[1],
		D2Count:               depthCounts[2],
	}, nil
}

// CompactionResult holds the results of compaction.
type CompactionResult struct {
	Summary               string
	OriginalTokens        int
	SummaryTokens         int
	TokensSaved           int
	Nodes                 []SummaryNode
	RootNodes             []SummaryNode
	TailMessages          []TranscriptMessage
	CoveredUntilMessageID int64
	D0Count               int
	D1Count               int
	D2Count               int
}

// FormatNotification formats the compaction result for display.
func (r *CompactionResult) FormatNotification() string {
	return fmt.Sprintf(
		"🗜️ Context compacted\nTokens saved: %d → %d (%d saved)\nTree: D0=%d D1=%d D2=%d, raw tail=%d",
		r.OriginalTokens,
		r.SummaryTokens,
		r.TokensSaved,
		r.D0Count,
		r.D1Count,
		r.D2Count,
		len(r.TailMessages),
	)
}

// CompactSession compacts a session and returns the new session content.
func (c *Compactor) CompactSession(ctx context.Context, currentSession string) (string, error) {
	if currentSession == "" {
		return "", nil
	}

	messages := []ai.Message{
		{Role: "assistant", Content: currentSession},
	}

	result, err := c.Compact(ctx, messages)
	if err != nil {
		return "", err
	}

	return result.Summary, nil
}

func (c *Compactor) chunkTranscript(messages []TranscriptMessage) [][]TranscriptMessage {
	target := c.treeConfig.d0ChunkTargetToken
	if target <= 0 {
		target = defaultD0ChunkTargetToken
	}

	var chunks [][]TranscriptMessage
	var chunk []TranscriptMessage
	chunkTokens := 0

	for _, msg := range messages {
		msgTokens := c.tokenCounter.CountTokens(msg.Content) + 4
		if len(chunk) > 0 && chunkTokens+msgTokens > target {
			chunks = append(chunks, chunk)
			chunk = nil
			chunkTokens = 0
		}
		chunk = append(chunk, msg)
		chunkTokens += msgTokens
	}
	if len(chunk) > 0 {
		chunks = append(chunks, chunk)
	}
	return chunks
}

func (c *Compactor) summarizeTranscriptChunk(ctx context.Context, chunk []TranscriptMessage) (string, error) {
	var conversation strings.Builder
	for _, msg := range chunk {
		conversation.WriteString(fmt.Sprintf("\n%s: %s\n", msg.Role, msg.Content))
	}

	return c.completeSummary(ctx,
		`You are building a D0 context node from a raw conversation transcript.

Rules:
1. Keep concrete facts, names, dates, files, commands, and decisions.
2. Preserve unfinished work, blockers, and explicit user preferences.
3. Remove repetition and filler, but do not invent missing details.
4. Write a compact paragraph followed by short bullet-like lines when useful.
5. This node must stand alone without the raw messages.`,
		conversation.String(),
	)
}

func (c *Compactor) summarizeSummaryGroup(ctx context.Context, depth int, group []SummaryNode) (string, error) {
	var input strings.Builder
	for _, node := range group {
		input.WriteString(fmt.Sprintf(
			"\n[%s transcript:%d-%d]\n%s\n",
			node.Key,
			node.SourceStartID,
			node.SourceEndID,
			node.Content,
		))
	}

	return c.completeSummary(ctx,
		fmt.Sprintf(`You are building a D%d context node from lower-level summaries.

Rules:
1. Merge overlapping details without dropping concrete facts.
2. Keep only information that still matters for future reasoning.
3. Preserve the chronology and ownership of decisions.
4. Write a concise synthesis that can replace the child summaries in context.
5. Do not mention internal instructions or speculate beyond the inputs.`, depth),
		input.String(),
	)
}

func (c *Compactor) completeSummary(ctx context.Context, systemPrompt, input string) (string, error) {
	summary, err := c.aiClient.Complete(ctx, []ai.Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: strings.TrimSpace(input)},
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate summary: %w", err)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return "", fmt.Errorf("failed to generate summary: empty response")
	}
	return summary, nil
}

func (c *Compactor) countTranscriptTokens(messages []TranscriptMessage) int {
	msgs := make([]Message, 0, len(messages))
	for _, msg := range messages {
		msgs = append(msgs, Message{Role: msg.Role, Content: msg.Content})
	}
	return c.tokenCounter.CountMessages(msgs)
}

func (c *Compactor) countSummaryContextTokens(roots []SummaryNode, tail []TranscriptMessage) int {
	msgs := make([]Message, 0, len(roots)+len(tail))
	for _, node := range roots {
		msgs = append(msgs, Message{
			Role:    "assistant",
			Content: fmt.Sprintf("[Compacted context D%d %d-%d]\n%s", node.Depth, node.SourceStartID, node.SourceEndID, node.Content),
		})
	}
	for _, msg := range tail {
		msgs = append(msgs, Message{Role: msg.Role, Content: msg.Content})
	}
	return c.tokenCounter.CountMessages(msgs)
}

func formatNodeKey(depth, ordinal int) string {
	return fmt.Sprintf("d%d:%04d", depth, ordinal)
}

func groupSummaryNodes(nodes []SummaryNode, fanout int) [][]SummaryNode {
	if fanout <= 0 {
		fanout = defaultSummaryFanout
	}

	var groups [][]SummaryNode
	for start := 0; start < len(nodes); start += fanout {
		end := start + fanout
		if end > len(nodes) {
			end = len(nodes)
		}
		groups = append(groups, nodes[start:end])
	}
	return groups
}
