package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPatchTool_Execute(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Create test file
	testFile := filepath.Join(tmpDir, "test.txt")
	originalContent := `line 1
line 2
line 3
line 4
line 5
`
	if err := os.WriteFile(testFile, []byte(originalContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tests := []struct {
		name          string
		patch         string
		expectedLines []string
		wantErr       bool
	}{
		{
			name: "add single line",
			patch: `--- a/test.txt
+++ b/test.txt
@@ -2,3 +2,4 @@
 line 2
+new line
 line 3
 line 4
`,
			expectedLines: []string{"line 1", "line 2", "new line", "line 3", "line 4", "line 5"},
			wantErr:       false,
		},
		{
			name: "remove single line",
			patch: `--- a/test.txt
+++ b/test.txt
@@ -1,5 +1,4 @@
 line 1
-line 2
 line 3
 line 4
 line 5
`,
			expectedLines: []string{"line 1", "line 3", "line 4", "line 5"},
			wantErr:       false,
		},
		{
			name: "replace line",
			patch: `--- a/test.txt
+++ b/test.txt
@@ -1,5 +1,5 @@
 line 1
-line 2
+modified line 2
 line 3
 line 4
 line 5
`,
			expectedLines: []string{"line 1", "modified line 2", "line 3", "line 4", "line 5"},
			wantErr:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset file to original content
			if err := os.WriteFile(testFile, []byte(originalContent), 0644); err != nil {
				t.Fatalf("Failed to reset test file: %v", err)
			}

			// Create patch tool
			tool := NewPatchTool(tmpDir)

			// Execute patch
			_, err := tool.Execute(context.Background(), "test.txt", tt.patch)
			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			// Read modified file
			content, err := os.ReadFile(testFile)
			if err != nil {
				t.Fatalf("Failed to read modified file: %v", err)
			}

			lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")

			// Verify content
			if len(lines) != len(tt.expectedLines) {
				t.Errorf("Expected %d lines, got %d", len(tt.expectedLines), len(lines))
				t.Logf("Expected: %v", tt.expectedLines)
				t.Logf("Got: %v", lines)
				return
			}

			for i, line := range lines {
				if line != tt.expectedLines[i] {
					t.Errorf("Line %d: expected %q, got %q", i+1, tt.expectedLines[i], line)
				}
			}
		})
	}
}

func TestPatchTool_SecurityCheck(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file outside the base path
	outsideDir := filepath.Join(tmpDir, "outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("Failed to create outside dir: %v", err)
	}

	insideDir := filepath.Join(tmpDir, "inside")
	if err := os.MkdirAll(insideDir, 0755); err != nil {
		t.Fatalf("Failed to create inside dir: %v", err)
	}

	tool := NewPatchTool(insideDir)

	// Try to patch a file outside the base path
	patch := `--- a/test.txt
+++ b/test.txt
@@ -1 +1 @@
-old
+new
`
	_, err := tool.Execute(context.Background(), "../outside/test.txt", patch)
	if err == nil {
		t.Error("Expected error for path outside base directory, got nil")
	}
	if !strings.Contains(err.Error(), "outside allowed directory") {
		t.Errorf("Expected 'outside allowed directory' error, got: %v", err)
	}
}

func TestParseHunkHeader(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected Hunk
		wantErr  bool
	}{
		{
			name:   "simple hunk",
			header: "@@ -1,3 +1,4 @@",
			expected: Hunk{
				OldStart: 1,
				OldCount: 3,
				NewStart: 1,
				NewCount: 4,
			},
			wantErr: false,
		},
		{
			name:   "hunk with single line",
			header: "@@ -5 +5,2 @@",
			expected: Hunk{
				OldStart: 5,
				OldCount: 1,
				NewStart: 5,
				NewCount: 2,
			},
			wantErr: false,
		},
		{
			name:     "invalid header",
			header:   "@@ invalid @@",
			expected: Hunk{},
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hunk, err := parseHunkHeader(tt.header)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseHunkHeader() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			if hunk.OldStart != tt.expected.OldStart ||
				hunk.OldCount != tt.expected.OldCount ||
				hunk.NewStart != tt.expected.NewStart ||
				hunk.NewCount != tt.expected.NewCount {
				t.Errorf("parseHunkHeader() = %+v, want %+v", hunk, tt.expected)
			}
		})
	}
}

func TestParsePatch(t *testing.T) {
	patch := `--- a/test.txt
+++ b/test.txt
@@ -1,3 +1,4 @@
 line 1
+new line
 line 2
 line 3
@@ -5,2 +6,2 @@
 line 5
-line 6
+modified line 6
`

	hunks, err := parsePatch(patch)
	if err != nil {
		t.Fatalf("parsePatch() error = %v", err)
	}

	if len(hunks) != 2 {
		t.Errorf("Expected 2 hunks, got %d", len(hunks))
	}

	// Check first hunk
	if hunks[0].OldStart != 1 || hunks[0].OldCount != 3 {
		t.Errorf("First hunk: expected OldStart=1, OldCount=3, got %d, %d",
			hunks[0].OldStart, hunks[0].OldCount)
	}

	if len(hunks[0].Lines) != 4 {
		t.Errorf("First hunk: expected 4 lines, got %d", len(hunks[0].Lines))
	}

	// Check second hunk
	if hunks[1].OldStart != 5 || hunks[1].OldCount != 2 {
		t.Errorf("Second hunk: expected OldStart=5, OldCount=2, got %d, %d",
			hunks[1].OldStart, hunks[1].OldCount)
	}

	if len(hunks[1].Lines) != 3 {
		t.Errorf("Second hunk: expected 3 lines, got %d", len(hunks[1].Lines))
	}
}
