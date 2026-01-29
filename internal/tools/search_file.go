package tools

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SearchFileTool searches for patterns in files
type SearchFileTool struct {
	BasePath string
}

// NewSearchFileTool creates a new file search tool
func NewSearchFileTool(basePath string) *SearchFileTool {
	return &SearchFileTool{BasePath: basePath}
}

func (s *SearchFileTool) Name() string {
	return "grep"
}

func (s *SearchFileTool) Description() string {
	return "Search for a pattern in files. Args: pattern [directory]. Searches recursively for text pattern in files."
}

// GetSchema returns the JSON Schema for grep tool parameters
func (s *SearchFileTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"input": map[string]interface{}{
				"type":        "string",
				"description": "Regex pattern to search for",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Subdirectory to search in (optional, relative to base)",
			},
		},
		"required": []string{"input"},
	}
}

func (s *SearchFileTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: grep <pattern> [directory]")
	}

	pattern := args[0]
	searchDir := s.BasePath
	if len(args) > 1 {
		searchDir = filepath.Join(s.BasePath, args[1])
	}

	// Ensure search directory is within base path (security)
	if s.BasePath != "" && !strings.HasPrefix(searchDir, s.BasePath) {
		return "", fmt.Errorf("search path outside allowed directory")
	}

	// Compile regex pattern
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}

	// Search for matches
	matches, err := s.searchFiles(searchDir, re)
	if err != nil {
		return "", fmt.Errorf("search failed: %w", err)
	}

	if len(matches) == 0 {
		return "No matches found", nil
	}

	// Format results
	result := fmt.Sprintf("Found %d matches:\n", len(matches))
	for _, match := range matches {
		result += match + "\n"
	}

	return result, nil
}

// searchFiles recursively searches files for the pattern
func (s *SearchFileTool) searchFiles(dir string, pattern *regexp.Regexp) ([]string, error) {
	var matches []string
	const maxMatches = 50

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors and continue
		}

		// Skip directories to ignore
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".idea" ||
				name == "__pycache__" || name == ".venv" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip if we've reached max matches
		if len(matches) >= maxMatches {
			return filepath.SkipDir
		}

		// Only search text files (skip binary files)
		if !isTextFile(path) {
			return nil
		}

		// Search file content
		fileMatches, err := s.searchFile(path, pattern)
		if err != nil {
			return nil // Skip files with errors
		}

		matches = append(matches, fileMatches...)

		// Stop if we've reached max matches
		if len(matches) >= maxMatches {
			return filepath.SkipDir
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	// Limit to maxMatches
	if len(matches) > maxMatches {
		matches = matches[:maxMatches]
	}

	return matches, nil
}

// searchFile searches a single file for the pattern
func (s *SearchFileTool) searchFile(path string, pattern *regexp.Regexp) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var matches []string
	scanner := bufio.NewScanner(file)
	lineNum := 1

	for scanner.Scan() {
		line := scanner.Text()
		if pattern.MatchString(line) {
			// Format: filepath:line: content
			relPath := path
			if s.BasePath != "" {
				relPath, _ = filepath.Rel(s.BasePath, path)
			}
			matches = append(matches, fmt.Sprintf("%s:%d: %s", relPath, lineNum, line))
		}
		lineNum++
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return matches, nil
}

// isTextFile checks if a file is likely a text file
func isTextFile(path string) bool {
	// Check extension
	ext := strings.ToLower(filepath.Ext(path))
	textExts := map[string]bool{
		".go":   true,
		".txt":  true,
		".md":   true,
		".json": true,
		".yaml": true,
		".yml":  true,
		".toml": true,
		".xml":  true,
		".html": true,
		".css":  true,
		".js":   true,
		".ts":   true,
		".py":   true,
		".rb":   true,
		".java": true,
		".c":    true,
		".cpp":  true,
		".h":    true,
		".sh":   true,
		".bash": true,
		".rs":   true,
		".sql":  true,
		".env":  true,
		".conf": true,
		".ini":  true,
		".log":  true,
		".csv":  true,
		"":      true, // Files without extension (like Makefile, Dockerfile)
	}

	if textExts[ext] {
		return true
	}

	// For files without extension, check if the name suggests it's a text file
	name := filepath.Base(path)
	textNames := []string{
		"Makefile", "Dockerfile", "README", "LICENSE", "CHANGELOG",
		"CONTRIBUTING", "AUTHORS", "NOTICE", "TODO", "ROADMAP",
	}

	for _, textName := range textNames {
		if strings.HasPrefix(name, textName) {
			return true
		}
	}

	return false
}
