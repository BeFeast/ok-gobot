package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"ok-gobot/internal/memory"
)

// MemoryTool provides semantic memory operations
type MemoryTool struct {
	manager *memory.MemoryManager
}

// NewMemoryTool creates a new memory tool
func NewMemoryTool(manager *memory.MemoryManager) *MemoryTool {
	return &MemoryTool{
		manager: manager,
	}
}

func (m *MemoryTool) Name() string {
	return "memory"
}

func (m *MemoryTool) Description() string {
	return "Save or search long-term memories. Commands: save <text>, search <query>, list, forget <id>"
}

func (m *MemoryTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: memory <save|search|list|forget> [args]")
	}

	command := args[0]

	switch command {
	case "save":
		return m.executeSave(ctx, args[1:])
	case "search":
		return m.executeSearch(ctx, args[1:])
	case "list":
		return m.executeList(ctx)
	case "forget":
		return m.executeForget(ctx, args[1:])
	default:
		return "", fmt.Errorf("unknown command: %s", command)
	}
}

func (m *MemoryTool) executeSave(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: memory save <text> [--category=<category>]")
	}

	// Parse content and category
	var content string
	category := "general"

	for _, arg := range args {
		if strings.HasPrefix(arg, "--category=") {
			category = strings.TrimPrefix(arg, "--category=")
		} else {
			if content != "" {
				content += " "
			}
			content += arg
		}
	}

	if content == "" {
		return "", fmt.Errorf("no content to save")
	}

	if err := m.manager.Remember(ctx, content, category); err != nil {
		return "", fmt.Errorf("failed to save memory: %w", err)
	}

	return fmt.Sprintf("Memory saved in category '%s'", category), nil
}

func (m *MemoryTool) executeSearch(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: memory search <query> [--limit=<n>] [--person=<name>]")
	}

	// Parse query, limit, and metadata filter
	var query string
	limit := 5
	filter := memory.MemorySearchFilter{}

	for _, arg := range args {
		if strings.HasPrefix(arg, "--limit=") {
			limitStr := strings.TrimPrefix(arg, "--limit=")
			if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
				limit = n
			}
		} else if strings.HasPrefix(arg, "--person=") {
			filter.Person = strings.TrimSpace(strings.TrimPrefix(arg, "--person="))
		} else if strings.HasPrefix(arg, "--filter=") {
			filterJSON := strings.TrimPrefix(arg, "--filter=")
			var rawFilter struct {
				Person string `json:"person"`
			}
			if err := json.Unmarshal([]byte(filterJSON), &rawFilter); err == nil {
				if rawFilter.Person != "" {
					filter.Person = rawFilter.Person
				}
			}
		} else {
			if query != "" {
				query += " "
			}
			query += arg
		}
	}

	if query == "" {
		return "", fmt.Errorf("no query provided")
	}

	results, err := m.manager.RecallWithFilter(ctx, query, limit, filter)
	if err != nil {
		return "", fmt.Errorf("failed to search memories: %w", err)
	}

	if len(results) == 0 {
		return "No memories found matching your query.", nil
	}

	var output strings.Builder
	output.WriteString(fmt.Sprintf("Found %d relevant memories:\n\n", len(results)))
	for i, result := range results {
		output.WriteString(fmt.Sprintf("%d. [ID: %d] (similarity: %.2f) [%s]\n",
			i+1, result.ID, result.Similarity, result.Category))
		output.WriteString(fmt.Sprintf("   %s\n", result.Content))
		if len(result.Metadata.People) > 0 {
			output.WriteString(fmt.Sprintf("   People: %s\n", strings.Join(result.Metadata.People, ", ")))
		}
		if len(result.Metadata.Topics) > 0 {
			output.WriteString(fmt.Sprintf("   Topics: %s\n", strings.Join(result.Metadata.Topics, ", ")))
		}
		if len(result.Metadata.ActionItems) > 0 {
			output.WriteString(fmt.Sprintf("   Action items: %s\n", strings.Join(result.Metadata.ActionItems, " | ")))
		}
		if result.Metadata.Type != "" {
			output.WriteString(fmt.Sprintf("   Type: %s\n", result.Metadata.Type))
		}
		output.WriteString(fmt.Sprintf("   Created: %s\n\n", result.CreatedAt.Format("2006-01-02 15:04")))
	}

	return output.String(), nil
}

func (m *MemoryTool) executeList(ctx context.Context) (string, error) {
	results, err := m.manager.ListRecent(10)
	if err != nil {
		return "", fmt.Errorf("failed to list memories: %w", err)
	}

	if len(results) == 0 {
		return "No memories stored yet.", nil
	}

	var output strings.Builder
	output.WriteString(fmt.Sprintf("Recent memories (%d):\n\n", len(results)))
	for i, result := range results {
		output.WriteString(fmt.Sprintf("%d. [ID: %d] [%s]\n",
			i+1, result.ID, result.Category))
		output.WriteString(fmt.Sprintf("   %s\n", result.Content))
		output.WriteString(fmt.Sprintf("   Created: %s\n\n", result.CreatedAt.Format("2006-01-02 15:04")))
	}

	return output.String(), nil
}

func (m *MemoryTool) executeForget(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: memory forget <id>")
	}

	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid memory ID: %s", args[0])
	}

	if err := m.manager.ForgetByID(id); err != nil {
		return "", fmt.Errorf("failed to forget memory: %w", err)
	}

	return fmt.Sprintf("Memory %d forgotten.", id), nil
}

func (m *MemoryTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"save", "search", "list", "forget"},
				"description": "The memory operation to perform",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Content to save (for save command)",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query (for search command)",
			},
			"category": map[string]interface{}{
				"type":        "string",
				"description": "Category for saving memories (optional, defaults to 'general')",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Number of results to return (for search command, defaults to 5)",
			},
			"id": map[string]interface{}{
				"type":        "integer",
				"description": "Memory ID to forget (for forget command)",
			},
			"filter": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"person": map[string]interface{}{
						"type":        "string",
						"description": "Filter search results by person name from extracted metadata",
					},
				},
				"additionalProperties": false,
			},
			"person": map[string]interface{}{
				"type":        "string",
				"description": "Shortcut for filter.person when searching",
			},
		},
		"required": []string{"command"},
	}
}
