package bootstrap

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

type publicTreePrivacyRule struct {
	marker       string
	allowedPaths map[string]bool
}

func TestTrackedPublicTreeContainsNoPrivateDeploymentMarkers(t *testing.T) {
	repoRoot := publicTreeRepoRoot(t)
	tracked := exec.Command("git", "ls-files", "-z")
	tracked.Dir = repoRoot
	output, err := tracked.Output()
	if err != nil {
		t.Fatalf("list tracked repository files: %v", err)
	}

	join := func(parts ...string) string { return strings.Join(parts, "") }
	fixture := filepath.ToSlash("internal/bootstrap/scaffold_privacy_test.go")
	rules := []publicTreePrivacyRule{
		{
			marker: join("kos", "soy"),
			allowedPaths: map[string]bool{
				"LICENSE": true,
				fixture:   true,
			},
		},
		{
			marker:       join("shtr", "udel"),
			allowedPaths: map[string]bool{},
		},
		{
			marker: join("shra", "ga"),
			allowedPaths: map[string]bool{
				"docs/memory-audit-droid.md": true, // Historical attribution, not a runtime default.
			},
		},
		{
			marker: join("10.10", ".0."),
			allowedPaths: map[string]bool{
				fixture: true,
			},
		},
		{
			marker: join(".ok", ".labs"),
		},
	}

	for _, rawPath := range bytes.Split(output, []byte{0}) {
		if len(rawPath) == 0 {
			continue
		}
		relPath := filepath.ToSlash(string(rawPath))
		content, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(relPath)))
		if err != nil {
			if os.IsNotExist(err) {
				// A concurrent integration lane may have removed a cached path.
				// Once committed, git ls-files no longer returns it.
				continue
			}
			t.Errorf("read tracked file %s: %v", relPath, err)
			continue
		}
		if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
			continue
		}

		for _, rule := range rules {
			if rule.allowedPaths[relPath] {
				continue
			}
			for lineNumber, line := range strings.Split(strings.ToLower(string(content)), "\n") {
				if strings.Contains(line, strings.ToLower(rule.marker)) {
					t.Errorf("tracked public file %s:%d contains private marker %q", relPath, lineNumber+1, rule.marker)
				}
			}
		}
	}
}

func publicTreeRepoRoot(t *testing.T) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve privacy test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
}
