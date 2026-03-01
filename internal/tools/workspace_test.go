package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolvePath verifies that resolvePath correctly joins relative paths,
// validates absolute paths, and rejects traversal attempts.
func TestResolvePath(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name          string
		workspaceRoot string
		path          string
		wantSuffix    string // expected suffix of returned path
		wantErr       bool
		errContains   string
	}{
		{
			name:          "relative path joined with root",
			workspaceRoot: tmpDir,
			path:          "subdir/file.txt",
			wantSuffix:    filepath.Join(tmpDir, "subdir/file.txt"),
		},
		{
			name:          "absolute path inside root",
			workspaceRoot: tmpDir,
			path:          filepath.Join(tmpDir, "file.txt"),
			wantSuffix:    filepath.Join(tmpDir, "file.txt"),
		},
		{
			name:          "traversal via relative path blocked",
			workspaceRoot: tmpDir,
			path:          "../escape.txt",
			wantErr:       true,
			errContains:   "outside allowed directory",
		},
		{
			name:          "absolute path outside root blocked",
			workspaceRoot: tmpDir,
			path:          "/etc/passwd",
			wantErr:       true,
			errContains:   "outside allowed directory",
		},
		{
			name:          "empty root returns path as-is (relative)",
			workspaceRoot: "",
			path:          "relative.txt",
			wantSuffix:    "relative.txt",
		},
		{
			name:          "root itself is allowed",
			workspaceRoot: tmpDir,
			path:          ".",
			wantSuffix:    filepath.Clean(tmpDir),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePath(tt.workspaceRoot, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Errorf("resolvePath() expected error, got nil")
					return
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("resolvePath() error = %q, want to contain %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Errorf("resolvePath() unexpected error: %v", err)
				return
			}
			if got != tt.wantSuffix {
				t.Errorf("resolvePath() = %q, want %q", got, tt.wantSuffix)
			}
		})
	}
}

// TestFileTool_WorkspaceResolution verifies that FileTool reads and writes
// correctly when the process is not started from the workspace directory.
// This simulates the case where the binary is started from /tmp but the
// workspace is elsewhere.
func TestFileTool_WorkspaceResolution(t *testing.T) {
	// Create workspace in a temp dir (simulates soul_path)
	workspace := t.TempDir()

	// Write a file into the workspace
	content := "hello workspace\n"
	if err := os.WriteFile(filepath.Join(workspace, "hello.txt"), []byte(content), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := &FileTool{BasePath: workspace}

	// Read with a relative path — must resolve against workspace, not process cwd
	got, err := tool.Read("hello.txt")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got != content {
		t.Errorf("Read() = %q, want %q", got, content)
	}

	// Write with a relative path
	if err := tool.Write("out.txt", "written"); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	written, err := os.ReadFile(filepath.Join(workspace, "out.txt"))
	if err != nil {
		t.Fatalf("ReadFile after Write: %v", err)
	}
	if string(written) != "written" {
		t.Errorf("Write() wrote %q, want %q", string(written), "written")
	}

	// Traversal attempt must be blocked
	_, err = tool.Read("../secret.txt")
	if err == nil {
		t.Error("Read() expected error for traversal path, got nil")
	}
}

// TestPatchTool_WorkspaceResolution verifies that PatchTool resolves relative
// paths against the configured workspace root.
func TestPatchTool_WorkspaceResolution(t *testing.T) {
	workspace := t.TempDir()

	original := "line1\nline2\nline3\n"
	if err := os.WriteFile(filepath.Join(workspace, "target.txt"), []byte(original), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := NewPatchTool(workspace)

	patch := `--- a/target.txt
+++ b/target.txt
@@ -1,3 +1,3 @@
 line1
-line2
+LINE2
 line3
`
	_, err := tool.Execute(context.Background(), "target.txt", patch)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	result, err := os.ReadFile(filepath.Join(workspace, "target.txt"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(result), "LINE2") {
		t.Errorf("patch not applied, content: %q", string(result))
	}
}

// TestSearchFileTool_WorkspaceResolution verifies that SearchFileTool resolves
// relative subdirectory paths against the configured workspace root.
func TestSearchFileTool_WorkspaceResolution(t *testing.T) {
	workspace := t.TempDir()

	// Create a file inside the workspace
	if err := os.WriteFile(filepath.Join(workspace, "find_me.txt"), []byte("needle\n"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tool := NewSearchFileTool(workspace)

	// Search without a subdirectory — should find file in workspace
	result, err := tool.Execute(context.Background(), "needle")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(result, "find_me.txt") {
		t.Errorf("expected find_me.txt in results, got: %s", result)
	}

	// Traversal attempt must be blocked
	_, err = tool.Execute(context.Background(), "needle", "../outside")
	if err == nil {
		t.Error("Execute() expected error for traversal path, got nil")
	}
	if !strings.Contains(err.Error(), "outside allowed directory") {
		t.Errorf("unexpected error: %v", err)
	}
}
