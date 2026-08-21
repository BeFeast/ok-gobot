package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"ok-gobot/internal/memory"
)

// MemorySearchTool performs hybrid lexical and semantic search over indexed markdown memory chunks.
type MemorySearchTool struct {
	manager *memory.MemoryManager
	policy  *memory.RecallPolicy
}

// NewMemorySearchTool creates a memory_search tool.
func NewMemorySearchTool(manager *memory.MemoryManager) *MemorySearchTool {
	return &MemorySearchTool{manager: manager}
}

// NewScopedMemorySearchTool creates a memory_search tool constrained by policy.
func NewScopedMemorySearchTool(manager *memory.MemoryManager, policy *memory.RecallPolicy) *MemorySearchTool {
	return &MemorySearchTool{manager: manager, policy: policy}
}

func (m *MemorySearchTool) Name() string {
	return "memory_search"
}

func (m *MemorySearchTool) Description() string {
	return "Hybrid lexical and semantic search over indexed markdown memory chunks."
}

func (m *MemorySearchTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("usage: memory_search <query> [limit] [expand]")
	}
	if m.manager == nil {
		return "", fmt.Errorf("memory manager is not configured")
	}

	query := strings.TrimSpace(args[0])
	limit := 5
	if len(args) > 1 {
		n, err := strconv.Atoi(strings.TrimSpace(args[1]))
		if err == nil && n > 0 {
			limit = n
		}
	}

	expand := false
	if len(args) > 2 {
		expand = strings.EqualFold(strings.TrimSpace(args[2]), "true")
	}

	var results []memory.MemoryResult
	var err error
	if expand {
		results, err = m.manager.SearchExpandedScoped(ctx, query, limit, m.policy)
	} else {
		results, err = m.manager.SearchScoped(ctx, query, limit, m.policy)
	}
	if err != nil {
		return "", fmt.Errorf("failed to search memory index: %w", err)
	}

	if len(results) == 0 {
		if m.policy != nil {
			return m.policy.Summary() + "\n\nNo memory chunks found matching your query in the allowed scopes.", nil
		}
		return "No memory chunks found matching your query.", nil
	}

	label := "chunks"
	if expand {
		label = "branches"
	}

	var out strings.Builder
	if m.policy != nil {
		out.WriteString(m.policy.Summary())
		out.WriteString("\n\n")
	}
	out.WriteString(fmt.Sprintf("Found %d relevant memory %s:\n\n", len(results), label))
	for i, result := range results {
		headerPath := result.HeaderPath
		if headerPath == "" {
			headerPath = "(root)"
		}
		out.WriteString(fmt.Sprintf("%d. Source: %s\n", i+1, result.Source))
		out.WriteString(fmt.Sprintf("   Header Path: %s\n", headerPath))
		if !expand {
			out.WriteString(fmt.Sprintf("   Lines: %d-%d\n", result.StartLine, result.EndLine))
			out.WriteString(fmt.Sprintf("   Chunk: %d\n", result.ChunkOrdinal))
		}
		out.WriteString(fmt.Sprintf("   Similarity: %.2f\n", result.Similarity))
		if result.LexicalScore > 0 || result.VectorScore != 0 {
			out.WriteString(fmt.Sprintf("   Score Components: lexical=%.2f vector=%.2f\n", result.LexicalScore, result.VectorScore))
		}
		out.WriteString(fmt.Sprintf("   %s\n\n", memory.SanitizeSnippet(result.Content)))
	}

	return out.String(), nil
}

func (m *MemorySearchTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Natural-language query to search memory chunks",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of chunks to return (default 5)",
			},
			"expand": map[string]interface{}{
				"type":        "boolean",
				"description": "When true, expand each match to include the full branch (all chunks sharing the same source file and header path)",
			},
		},
		"required": []string{"query"},
	}
}

// ExecuteJSON (tool-schema disease, fixed fleet-wide 2026-08-21).
func (m *MemorySearchTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	query := firstNonEmpty(params["query"], params["q"], params["term"], params["input"])
	if query == "" {
		return "", fmt.Errorf("memory_search: 'query' is required")
	}
	args := []string{query}
	if l := params["limit"]; l != "" {
		args = append(args, l)
	}
	if e := params["expand"]; e != "" {
		args = append(args, e)
	}
	return m.Execute(ctx, args...)
}
