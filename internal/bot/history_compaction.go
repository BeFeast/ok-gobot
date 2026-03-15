package bot

import (
	"fmt"
	"sort"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/storage"
)

func summaryRoots(nodes []storage.SessionSummaryNode) []storage.SessionSummaryNode {
	if len(nodes) == 0 {
		return nil
	}

	maxDepth := nodes[0].Depth
	for _, node := range nodes[1:] {
		if node.Depth > maxDepth {
			maxDepth = node.Depth
		}
	}

	roots := make([]storage.SessionSummaryNode, 0, len(nodes))
	for _, node := range nodes {
		if node.Depth == maxDepth {
			roots = append(roots, node)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if roots[i].Ordinal != roots[j].Ordinal {
			return roots[i].Ordinal < roots[j].Ordinal
		}
		return roots[i].SourceStartMessageID < roots[j].SourceStartMessageID
	})
	return roots
}

func maxCoveredMessageID(nodes []storage.SessionSummaryNode) int64 {
	var maxID int64
	for _, node := range nodes {
		if node.SourceEndMessageID > maxID {
			maxID = node.SourceEndMessageID
		}
	}
	return maxID
}

func buildCompactedHistory(roots []storage.SessionSummaryNode, tail []storage.SessionMessageV2) []ai.ChatMessage {
	history := make([]ai.ChatMessage, 0, len(roots)+len(tail))
	history = append(history, summaryRootsToChatMessages(roots)...)
	for _, msg := range tail {
		history = append(history, ai.ChatMessage{Role: msg.Role, Content: msg.Content})
	}
	return history
}

func summaryRootsToChatMessages(roots []storage.SessionSummaryNode) []ai.ChatMessage {
	history := make([]ai.ChatMessage, 0, len(roots))
	for _, node := range roots {
		history = append(history, ai.ChatMessage{
			Role:    "assistant",
			Content: formatSummaryRoot(node),
		})
	}
	return history
}

func formatSummaryRoot(node storage.SessionSummaryNode) string {
	return fmt.Sprintf(
		"[Compacted context D%d; transcript %d-%d]\n%s",
		node.Depth,
		node.SourceStartMessageID,
		node.SourceEndMessageID,
		node.Content,
	)
}

func resultNodesToStorage(sessionKey string, result *agent.CompactionResult) []storage.SessionSummaryNode {
	nodes := make([]storage.SessionSummaryNode, 0, len(result.Nodes))
	for _, node := range result.Nodes {
		nodes = append(nodes, storage.SessionSummaryNode{
			SessionKey:           sessionKey,
			NodeKey:              node.Key,
			Depth:                node.Depth,
			Ordinal:              node.Ordinal,
			Content:              node.Content,
			SourceStartMessageID: node.SourceStartID,
			SourceEndMessageID:   node.SourceEndID,
			ChildStartKey:        node.ChildStartKey,
			ChildEndKey:          node.ChildEndKey,
		})
	}
	return nodes
}

func trimCompactedHistoryToTokenBudget(roots []storage.SessionSummaryNode, tail []storage.SessionMessageV2, model string) []ai.ChatMessage {
	const historyBudgetFraction = 0.40

	budget := int(float64(agent.ModelLimits(model)) * historyBudgetFraction)
	if budget <= 0 {
		return buildCompactedHistory(roots, tail)
	}

	rootHistory := summaryRootsToChatMessages(roots)
	if len(rootHistory) == 0 {
		return trimHistoryToBudget(buildCompactedHistory(nil, tail), budget)
	}

	rootTokens := countChatHistoryTokens(rootHistory)
	if rootTokens >= budget {
		return trimChatHistoryFront(rootHistory, budget)
	}

	trimmedTail := trimHistoryToBudget(buildCompactedHistory(nil, tail), budget-rootTokens)
	history := make([]ai.ChatMessage, 0, len(rootHistory)+len(trimmedTail))
	history = append(history, rootHistory...)
	history = append(history, trimmedTail...)
	return history
}
