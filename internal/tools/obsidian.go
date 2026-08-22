package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ObsidianTool provides access to Obsidian vault
type ObsidianTool struct {
	VaultPath string

	// index is the retrieval store backing the search operation. When nil,
	// search falls back to a term-decomposed filesystem scan.
	index VaultIndex
	// indexPrefix scopes index hits to the collection holding this vault.
	indexPrefix string
}

// NewObsidianTool creates a new Obsidian tool
func NewObsidianTool(vaultPath string) *ObsidianTool {
	vaultPath = strings.TrimSpace(vaultPath)
	if vaultPath != "" {
		vaultPath = filepath.Clean(vaultPath)
	}
	return &ObsidianTool{VaultPath: vaultPath}
}

func (o *ObsidianTool) Name() string {
	return "obsidian"
}

func (o *ObsidianTool) Description() string {
	return "Read and write Obsidian vault notes"
}

func (o *ObsidianTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: obsidian <read|write|list|search> <path-or-term> [content]")
	}

	operation := args[0]
	path := args[1]

	switch operation {
	case "read":
		return o.ReadNote(path)
	case "write":
		if len(args) < 3 {
			return "", fmt.Errorf("content required for write")
		}
		content := strings.Join(args[2:], " ")
		return "", o.WriteNote(path, content)
	case "list":
		return o.ListNotes(path)
	case "search":
		result, err := o.SearchNotes(ctx, path, DefaultVaultSearchLimit)
		if err != nil {
			return "", err
		}
		return result.Format(), nil
	default:
		return "", fmt.Errorf("unknown operation: %s", operation)
	}
}

// resolveVaultPath resolves and validates a path is within the vault, following symlinks.
func (o *ObsidianTool) resolveVaultPath(relativePath string) (string, error) {
	if o == nil || strings.TrimSpace(o.VaultPath) == "" {
		return "", fmt.Errorf("Obsidian vault directory is not configured")
	}
	relativePath = filepath.Clean(relativePath)
	if strings.HasPrefix(relativePath, "..") {
		return "", fmt.Errorf("invalid path: cannot traverse outside vault")
	}
	fullPath := filepath.Join(o.VaultPath, relativePath)

	resolvedVault, err := filepath.EvalSymlinks(o.VaultPath)
	if err != nil {
		resolvedVault = o.VaultPath
	}

	// For existing paths, resolve symlinks; for new paths, resolve parent.
	resolvedPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		parent, pErr := filepath.EvalSymlinks(filepath.Dir(fullPath))
		if pErr != nil {
			parent = filepath.Dir(fullPath)
		}
		resolvedPath = filepath.Join(parent, filepath.Base(fullPath))
	}

	rootWithSep := resolvedVault + string(os.PathSeparator)
	if resolvedPath != resolvedVault && !strings.HasPrefix(resolvedPath, rootWithSep) {
		return "", fmt.Errorf("path resolves outside vault")
	}
	return fullPath, nil
}

// ReadNote reads a note from the vault
func (o *ObsidianTool) ReadNote(relativePath string) (string, error) {
	fullPath, err := o.resolveVaultPath(relativePath)
	if err != nil {
		return "", err
	}

	// Extensionless paths are notes; explicit extensions such as .yaml are kept.
	if filepath.Ext(fullPath) == "" {
		fullPath += ".md"
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("note not found: %s", relativePath)
		}
		return "", err
	}

	return string(content), nil
}

// WriteNote writes a note to the vault
func (o *ObsidianTool) WriteNote(relativePath string, content string) error {
	fullPath, err := o.resolveVaultPath(relativePath)
	if err != nil {
		return err
	}

	// Extensionless paths are notes; explicit extensions such as .yaml are kept.
	if filepath.Ext(fullPath) == "" {
		fullPath += ".md"
	}

	// Ensure directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Markdown notes receive creation frontmatter; explicit non-Markdown files
	// such as routing YAML are written byte-for-byte.
	if strings.EqualFold(filepath.Ext(fullPath), ".md") && !strings.HasPrefix(content, "---") {
		frontmatter := fmt.Sprintf("---\ncreated: %s\n---\n\n",
			time.Now().Format("2006-01-02 15:04"))
		content = frontmatter + content
	}

	return os.WriteFile(fullPath, []byte(content), 0644)
}

// ListNotes lists notes in a directory
func (o *ObsidianTool) ListNotes(relativePath string) (string, error) {
	fullPath, err := o.resolveVaultPath(relativePath)
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return "", err
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Notes in %s:\n\n", relativePath))

	for _, entry := range entries {
		if entry.IsDir() {
			result.WriteString(fmt.Sprintf("📁 %s/\n", entry.Name()))
		} else if strings.HasSuffix(entry.Name(), ".md") {
			result.WriteString(fmt.Sprintf("📝 %s\n", strings.TrimSuffix(entry.Name(), ".md")))
		}
	}

	return result.String(), nil
}

// GetSchema exposes structured parameters. Without it the model only saw the
// generic single-string "input" schema, packed "write <path> <content>" into
// one argument, and every call died on the usage error — 13 times in a row in
// the 2026-08-21 telemetry before anyone could see why.
func (o *ObsidianTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"description": "What to do with the vault",
				"enum":        []string{"read", "write", "list", "search"},
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Note path relative to the vault root (read/write/list), e.g. daily/2026-08-21.md",
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Full note content for 'write'",
			},
			"term": map[string]interface{}{
				"type": "string",
				"description": "Search query for 'search'. Pass the natural-language question as-is; " +
					"it is split into content terms and notes are ranked by how many terms they match.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "Maximum number of notes to return for 'search' (default 10)",
			},
		},
		"required": []string{"operation"},
	}
}

// ExecuteJSON accepts named parameters directly, avoiding the lossy
// positional conversion entirely.
func (o *ObsidianTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	op := firstNonEmpty(params["operation"], params["command"], params["action"])
	path := firstNonEmpty(params["path"], params["note"], params["file"])
	content := firstNonEmpty(params["content"], params["text"], params["body"])
	term := firstNonEmpty(params["term"], params["query"], params["search"])

	switch op {
	case "read":
		if path == "" {
			return "", fmt.Errorf("obsidian read: 'path' is required")
		}
		return o.ReadNote(path)
	case "write":
		if path == "" {
			return "", fmt.Errorf("obsidian write: 'path' is required")
		}
		if content == "" {
			return "", fmt.Errorf("obsidian write: 'content' is required")
		}
		if err := o.WriteNote(path, content); err != nil {
			return "", err
		}
		return fmt.Sprintf("Note written: %s (%d bytes)", path, len(content)), nil
	case "list":
		return o.ListNotes(path)
	case "search":
		if term == "" {
			term = path
		}
		if term == "" {
			return "", fmt.Errorf("obsidian search: 'term' is required")
		}
		limit := DefaultVaultSearchLimit
		if raw := firstNonEmpty(params["limit"], params["top_k"], params["topk"]); raw != "" {
			if n, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
				limit = n
			}
		}
		result, err := o.SearchNotes(ctx, term, limit)
		if err != nil {
			return "", err
		}
		return result.Format(), nil
	case "":
		return "", fmt.Errorf("obsidian: 'operation' is required (read|write|list|search)")
	default:
		return "", fmt.Errorf("obsidian: unknown operation %q (read|write|list|search)", op)
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
