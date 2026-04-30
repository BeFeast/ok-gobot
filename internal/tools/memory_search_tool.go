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

// ScopeMemoryTools returns a registry where memory_search and memory_get enforce policy.
func ScopeMemoryTools(registry *Registry, policy *memory.RecallPolicy) *Registry {
	if registry == nil || policy == nil {
		return registry
	}
	scoped := registry.Child()
	for _, tool := range registry.List() {
		switch t := tool.(type) {
		case *MemorySearchTool:
			scoped.Register(NewScopedMemorySearchTool(t.manager, policy))
		case *MemoryGetTool:
			scoped.Register(t.WithRecallPolicy(policy))
		default:
			scoped.Register(tool)
		}
	}
	return scoped
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

	var search memory.RecallSearchResult
	var err error
	if expand {
		search, err = m.manager.SearchExpandedScoped(ctx, query, limit, m.policy)
	} else {
		search, err = m.manager.SearchScoped(ctx, query, limit, m.policy)
	}
	if err != nil {
		return "", fmt.Errorf("failed to search memory index: %w", err)
	}
	results := search.Results

	var out strings.Builder
	if m.policy != nil {
		writeRecallPolicySummary(&out, m.policy, search.Decisions)
	}
	if len(results) == 0 {
		out.WriteString("No memory chunks found matching your query.")
		return out.String(), nil
	}

	label := "chunks"
	if expand {
		label = "branches"
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
		out.WriteString(fmt.Sprintf("   %s\n\n", memory.RedactMemorySnippet(result.Content)))
	}

	return out.String(), nil
}

func writeRecallPolicySummary(out *strings.Builder, policy *memory.RecallPolicy, decisions []memory.RecallDecision) {
	allowed, denied := 0, 0
	deniedReasons := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Allowed {
			allowed++
			continue
		}
		denied++
		if len(deniedReasons) < 5 {
			label := string(decision.Scope)
			if decision.Label != "" {
				label += ":" + decision.Label
			}
			deniedReasons = append(deniedReasons, fmt.Sprintf("%s (%s)", label, decision.Reason))
		}
	}

	out.WriteString(policy.Describe())
	out.WriteString("\n")
	out.WriteString(fmt.Sprintf("policy decisions: allowed_sources=%d denied_sources=%d", allowed, denied))
	if len(deniedReasons) > 0 {
		out.WriteString(" denied=")
		out.WriteString(strings.Join(deniedReasons, "; "))
	}
	out.WriteString("\n\n")
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
