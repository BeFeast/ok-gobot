package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// PatchTool applies unified diff patches to files
type PatchTool struct {
	BasePath string
}

// NewPatchTool creates a new patch tool
func NewPatchTool(basePath string) *PatchTool {
	return &PatchTool{BasePath: basePath}
}

func (p *PatchTool) Name() string {
	return "patch"
}

func (p *PatchTool) Description() string {
	return "Apply a unified diff patch to a file. Args: filepath followed by the patch content in unified diff format."
}

func (p *PatchTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: patch <filepath> <patch-content>")
	}

	filepath := args[0]
	patchContent := strings.Join(args[1:], " ")

	// Parse and apply the patch
	if err := p.applyPatch(filepath, patchContent); err != nil {
		return "", fmt.Errorf("failed to apply patch: %w", err)
	}

	return fmt.Sprintf("Successfully applied patch to %s", filepath), nil
}

// applyPatch parses a unified diff and applies it to the target file
func (p *PatchTool) applyPatch(targetPath, patchContent string) error {
	// Ensure path is within base path (security)
	fullPath := filepath.Join(p.BasePath, targetPath)
	if p.BasePath != "" && !strings.HasPrefix(fullPath, p.BasePath) {
		return fmt.Errorf("path outside allowed directory")
	}

	// Read current file content
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	lines := strings.Split(string(content), "\n")

	// Parse the patch
	hunks, err := parsePatch(patchContent)
	if err != nil {
		return fmt.Errorf("failed to parse patch: %w", err)
	}

	// Apply hunks in reverse order to maintain line numbers
	for i := len(hunks) - 1; i >= 0; i-- {
		hunk := hunks[i]
		if err := applyHunk(&lines, hunk); err != nil {
			return fmt.Errorf("failed to apply hunk: %w", err)
		}
	}

	// Write the modified content back
	newContent := strings.Join(lines, "\n")
	if err := os.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// Hunk represents a single patch hunk
type Hunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Lines    []string // Lines with +/- prefix
}

// parsePatch parses unified diff format
func parsePatch(patchContent string) ([]Hunk, error) {
	var hunks []Hunk
	scanner := bufio.NewScanner(strings.NewReader(patchContent))

	var currentHunk *Hunk

	for scanner.Scan() {
		line := scanner.Text()

		// Skip file headers (--- and +++)
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}

		// Parse hunk header: @@ -start,count +start,count @@
		if strings.HasPrefix(line, "@@") {
			// Save previous hunk if exists
			if currentHunk != nil {
				hunks = append(hunks, *currentHunk)
			}

			hunk, err := parseHunkHeader(line)
			if err != nil {
				return nil, err
			}
			currentHunk = &hunk
			continue
		}

		// Add line to current hunk
		if currentHunk != nil {
			currentHunk.Lines = append(currentHunk.Lines, line)
		}
	}

	// Add last hunk
	if currentHunk != nil {
		hunks = append(hunks, *currentHunk)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return hunks, nil
}

// parseHunkHeader parses @@ -start,count +start,count @@
func parseHunkHeader(header string) (Hunk, error) {
	// Remove @@ prefix and suffix
	header = strings.TrimPrefix(header, "@@")
	header = strings.TrimSuffix(header, "@@")
	header = strings.TrimSpace(header)

	parts := strings.Fields(header)
	if len(parts) < 2 {
		return Hunk{}, fmt.Errorf("invalid hunk header: %s", header)
	}

	// Parse old range: -start,count
	oldRange := strings.TrimPrefix(parts[0], "-")
	oldParts := strings.Split(oldRange, ",")
	oldStart, err := strconv.Atoi(oldParts[0])
	if err != nil {
		return Hunk{}, fmt.Errorf("invalid old start: %s", oldParts[0])
	}
	oldCount := 1
	if len(oldParts) > 1 {
		oldCount, err = strconv.Atoi(oldParts[1])
		if err != nil {
			return Hunk{}, fmt.Errorf("invalid old count: %s", oldParts[1])
		}
	}

	// Parse new range: +start,count
	newRange := strings.TrimPrefix(parts[1], "+")
	newParts := strings.Split(newRange, ",")
	newStart, err := strconv.Atoi(newParts[0])
	if err != nil {
		return Hunk{}, fmt.Errorf("invalid new start: %s", newParts[0])
	}
	newCount := 1
	if len(newParts) > 1 {
		newCount, err = strconv.Atoi(newParts[1])
		if err != nil {
			return Hunk{}, fmt.Errorf("invalid new count: %s", newParts[1])
		}
	}

	return Hunk{
		OldStart: oldStart,
		OldCount: oldCount,
		NewStart: newStart,
		NewCount: newCount,
	}, nil
}

// applyHunk applies a single hunk to the lines
func applyHunk(lines *[]string, hunk Hunk) error {
	// Convert to 0-based index
	startIdx := hunk.OldStart - 1

	if startIdx < 0 || startIdx > len(*lines) {
		return fmt.Errorf("hunk start index out of range: %d", startIdx)
	}

	// Collect new lines and track position
	var newLines []string
	oldIdx := startIdx

	for _, line := range hunk.Lines {
		if len(line) == 0 {
			continue
		}

		prefix := line[0]
		content := ""
		if len(line) > 1 {
			content = line[1:]
		}

		switch prefix {
		case ' ':
			// Context line - verify and keep
			if oldIdx >= len(*lines) {
				return fmt.Errorf("context line beyond file end")
			}
			if (*lines)[oldIdx] != content {
				return fmt.Errorf("context mismatch at line %d: expected %q, got %q",
					oldIdx+1, content, (*lines)[oldIdx])
			}
			newLines = append(newLines, content)
			oldIdx++

		case '-':
			// Remove line - verify and skip
			if oldIdx >= len(*lines) {
				return fmt.Errorf("remove line beyond file end")
			}
			if (*lines)[oldIdx] != content {
				return fmt.Errorf("remove line mismatch at line %d: expected %q, got %q",
					oldIdx+1, content, (*lines)[oldIdx])
			}
			oldIdx++

		case '+':
			// Add line
			newLines = append(newLines, content)
		}
	}

	// Replace the old range with new lines
	endIdx := oldIdx

	// Build new file content
	result := make([]string, 0, len(*lines)-hunk.OldCount+hunk.NewCount)
	result = append(result, (*lines)[:startIdx]...)
	result = append(result, newLines...)
	result = append(result, (*lines)[endIdx:]...)

	*lines = result
	return nil
}
