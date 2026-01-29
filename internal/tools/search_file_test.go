package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchFileTool_Execute(t *testing.T) {
	// Create temp directory structure
	tmpDir := t.TempDir()

	// Create test files
	testFiles := map[string]string{
		"file1.txt": `first line
second line with pattern
third line
`,
		"file2.go": `package main

func TestFunction() {
	// pattern here
}
`,
		"subdir/file3.md": `# Title

This is a pattern match
Another line
`,
		"binary.bin": "\x00\x01\x02\x03", // Binary file to skip
	}

	for path, content := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("Failed to create directory: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create test file %s: %v", path, err)
		}
	}

	tests := []struct {
		name            string
		pattern         string
		dir             string
		expectedMatches int
		shouldContain   []string
		wantErr         bool
	}{
		{
			name:            "simple pattern",
			pattern:         "pattern",
			dir:             "",
			expectedMatches: 3,
			shouldContain:   []string{"file1.txt", "file2.go", "file3.md"},
			wantErr:         false,
		},
		{
			name:            "case sensitive pattern",
			pattern:         "Pattern",
			dir:             "",
			expectedMatches: 0,
			shouldContain:   []string{},
			wantErr:         false,
		},
		{
			name:            "regex pattern",
			pattern:         "pattern|Pattern",
			dir:             "",
			expectedMatches: 3,
			shouldContain:   []string{"file1.txt", "file2.go", "file3.md"},
			wantErr:         false,
		},
		{
			name:            "search in subdirectory",
			pattern:         "pattern",
			dir:             "subdir",
			expectedMatches: 1,
			shouldContain:   []string{"file3.md"},
			wantErr:         false,
		},
		{
			name:            "no matches",
			pattern:         "nonexistent",
			dir:             "",
			expectedMatches: 0,
			shouldContain:   []string{},
			wantErr:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewSearchFileTool(tmpDir)

			var result string
			var err error

			if tt.dir != "" {
				result, err = tool.Execute(context.Background(), tt.pattern, tt.dir)
			} else {
				result, err = tool.Execute(context.Background(), tt.pattern)
			}

			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			// Check if expected files are in results
			for _, expected := range tt.shouldContain {
				if !strings.Contains(result, expected) {
					t.Errorf("Expected result to contain %q, but it didn't. Result: %s", expected, result)
				}
			}

			// If we expect no matches
			if tt.expectedMatches == 0 && !strings.Contains(result, "No matches found") {
				t.Errorf("Expected 'No matches found', got: %s", result)
			}
		})
	}
}

func TestSearchFileTool_SecurityCheck(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory structure
	outsideDir := filepath.Join(tmpDir, "outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("Failed to create outside dir: %v", err)
	}

	insideDir := filepath.Join(tmpDir, "inside")
	if err := os.MkdirAll(insideDir, 0755); err != nil {
		t.Fatalf("Failed to create inside dir: %v", err)
	}

	tool := NewSearchFileTool(insideDir)

	// Try to search outside the base path
	_, err := tool.Execute(context.Background(), "test", "../outside")
	if err == nil {
		t.Error("Expected error for path outside base directory, got nil")
	}
	if !strings.Contains(err.Error(), "outside allowed directory") {
		t.Errorf("Expected 'outside allowed directory' error, got: %v", err)
	}
}

func TestSearchFileTool_SkipDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	// Create files in directories that should be skipped
	skipDirs := []string{".git", "node_modules", ".idea", "__pycache__", ".venv", "vendor"}

	for _, dir := range skipDirs {
		dirPath := filepath.Join(tmpDir, dir)
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			t.Fatalf("Failed to create directory %s: %v", dir, err)
		}
		filePath := filepath.Join(dirPath, "test.txt")
		if err := os.WriteFile(filePath, []byte("pattern here"), 0644); err != nil {
			t.Fatalf("Failed to create file in %s: %v", dir, err)
		}
	}

	// Create file that should be found
	validFile := filepath.Join(tmpDir, "valid.txt")
	if err := os.WriteFile(validFile, []byte("pattern here"), 0644); err != nil {
		t.Fatalf("Failed to create valid file: %v", err)
	}

	tool := NewSearchFileTool(tmpDir)
	result, err := tool.Execute(context.Background(), "pattern")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Should only find the valid file, not files in skip directories
	for _, dir := range skipDirs {
		if strings.Contains(result, dir) {
			t.Errorf("Result should not contain matches from %s directory", dir)
		}
	}

	if !strings.Contains(result, "valid.txt") {
		t.Error("Result should contain valid.txt")
	}
}

func TestIsTextFile(t *testing.T) {
	tests := []struct {
		name     string
		filename string
		expected bool
	}{
		{"Go file", "main.go", true},
		{"Text file", "notes.txt", true},
		{"Markdown", "README.md", true},
		{"JSON", "config.json", true},
		{"YAML", "docker-compose.yml", true},
		{"Python", "script.py", true},
		{"JavaScript", "app.js", true},
		{"Makefile", "Makefile", true},
		{"Dockerfile", "Dockerfile", true},
		{"Binary", "app.exe", false},
		{"Image", "photo.jpg", false},
		{"PDF", "document.pdf", false},
		{"License", "LICENSE", true},
		{"Env file", ".env", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isTextFile(tt.filename)
			if result != tt.expected {
				t.Errorf("isTextFile(%q) = %v, want %v", tt.filename, result, tt.expected)
			}
		})
	}
}

func TestSearchFileTool_MaxMatches(t *testing.T) {
	tmpDir := t.TempDir()

	// Create many files with matches (more than the 50 limit)
	for i := 0; i < 60; i++ {
		filename := filepath.Join(tmpDir, fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(filename, []byte("pattern match here"), 0644); err != nil {
			t.Fatalf("Failed to create test file: %v", err)
		}
	}

	tool := NewSearchFileTool(tmpDir)
	result, err := tool.Execute(context.Background(), "pattern")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Count matches in result
	matchCount := strings.Count(result, "file")
	if matchCount > 50 {
		t.Errorf("Expected maximum 50 matches, got %d", matchCount)
	}
}
