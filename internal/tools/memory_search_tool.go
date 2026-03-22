package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"ok-gobot/internal/memory"
)

// MemorySearchTool performs semantic search over indexed markdown memory chunks.
type MemorySearchTool struct {
	manager *memory.MemoryManager
}

// NewMemorySearchTool creates a memory_search tool.
func NewMemorySearchTool(manager *memory.MemoryManager) *MemorySearchTool {
	return &MemorySearchTool{manager: manager}
}

func (m *MemorySearchTool) Name() string {
	return "memory_search"
}

func (m *MemorySearchTool) Description() string {
	return "Semantic search over indexed markdown memory chunks."
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
		results, err = m.manager.SearchExpanded(ctx, query, limit)
	} else {
		results, err = m.manager.Search(ctx, query, limit)
	}
	if err != nil {
		return "", fmt.Errorf("failed to search memory index: %w", err)
	}

	if len(results) == 0 {
		return "No memory chunks found matching your query.", nil
	}

	label := "chunks"
	if expand {
		label = "branches"
	}

	var out strings.Builder
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
		}
		out.WriteString(fmt.Sprintf("   Similarity: %.2f\n", result.Similarity))
		out.WriteString(fmt.Sprintf("   %s\n\n", result.Content))
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
