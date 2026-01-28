package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ObsidianTool provides access to Obsidian vault
type ObsidianTool struct {
	VaultPath string
}

// NewObsidianTool creates a new Obsidian tool
func NewObsidianTool(vaultPath string) *ObsidianTool {
	if vaultPath == "" {
		// Default vault location
		homeDir, _ := os.UserHomeDir()
		vaultPath = filepath.Join(homeDir, "Obsidian")
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
		return "", fmt.Errorf("usage: obsidian <read|write|list> <path> [content]")
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
	default:
		return "", fmt.Errorf("unknown operation: %s", operation)
	}
}

// ReadNote reads a note from the vault
func (o *ObsidianTool) ReadNote(relativePath string) (string, error) {
	// Clean the path to prevent directory traversal
	relativePath = filepath.Clean(relativePath)
	if strings.HasPrefix(relativePath, "..") {
		return "", fmt.Errorf("invalid path: cannot traverse outside vault")
	}

	fullPath := filepath.Join(o.VaultPath, relativePath)

	// Ensure it's still within vault
	if !strings.HasPrefix(fullPath, o.VaultPath) {
		return "", fmt.Errorf("path outside vault")
	}

	// Add .md extension if not present
	if !strings.HasSuffix(fullPath, ".md") {
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
	// Clean the path
	relativePath = filepath.Clean(relativePath)
	if strings.HasPrefix(relativePath, "..") {
		return fmt.Errorf("invalid path: cannot traverse outside vault")
	}

	fullPath := filepath.Join(o.VaultPath, relativePath)

	// Ensure it's still within vault
	if !strings.HasPrefix(fullPath, o.VaultPath) {
		return fmt.Errorf("path outside vault")
	}

	// Add .md extension if not present
	if !strings.HasSuffix(fullPath, ".md") {
		fullPath += ".md"
	}

	// Ensure directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Add frontmatter if not present
	if !strings.HasPrefix(content, "---") {
		frontmatter := fmt.Sprintf("---\ncreated: %s\n---\n\n",
			time.Now().Format("2006-01-02 15:04"))
		content = frontmatter + content
	}

	return os.WriteFile(fullPath, []byte(content), 0644)
}

// ListNotes lists notes in a directory
func (o *ObsidianTool) ListNotes(relativePath string) (string, error) {
	fullPath := filepath.Join(o.VaultPath, relativePath)

	if !strings.HasPrefix(fullPath, o.VaultPath) {
		return "", fmt.Errorf("path outside vault")
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

// SearchNotes searches for notes containing a term
func (o *ObsidianTool) SearchNotes(term string) ([]string, error) {
	var matches []string

	err := filepath.Walk(o.VaultPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && strings.HasSuffix(path, ".md") {
			content, err := os.ReadFile(path)
			if err != nil {
				return nil // Skip files we can't read
			}

			if strings.Contains(string(content), term) {
				// Get relative path
				rel, _ := filepath.Rel(o.VaultPath, path)
				matches = append(matches, strings.TrimSuffix(rel, ".md"))
			}
		}

		return nil
	})

	return matches, err
}
