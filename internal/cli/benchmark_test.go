package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBenchmarkReliabilityCommandPrintsAndWritesReports(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "manifest.yaml")
	jsonPath := filepath.Join(dir, "report.json")
	markdownPath := filepath.Join(dir, "report.md")
	manifest := `
name: cli-fixture
version: 1
scenarios:
  - id: cli-pass
    provider: fake
    fake:
      outcome: merge_ready
      events:
        - state: issue_selected
          status: passed
        - state: preflight_passed
          status: passed
        - state: branch_created
          status: passed
        - state: pr_opened
          status: passed
        - state: ci_checked
          status: passed
        - state: review_checked
          status: passed
        - state: merge_ready_emitted
          status: passed
  - id: cli-ci-fail
    provider: fake
    fake:
      outcome: blocked
      failure_category: ci_failure
      reason: ci failed in CLI fixture
      events:
        - state: issue_selected
          status: passed
        - state: preflight_passed
          status: passed
        - state: branch_created
          status: passed
        - state: pr_opened
          status: passed
        - state: ci_checked
          status: failed
        - state: blocker_emitted
          status: failed
`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	cmd := newBenchmarkCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{
		"reliability",
		"--manifest", manifestPath,
		"--json-out", jsonPath,
		"--markdown-out", markdownPath,
	})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{"Reliability benchmark: cli-fixture", "PASS 1  FAIL 1  SKIP 0", "ci_failure=1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}

	jsonBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read JSON report: %v", err)
	}
	if !strings.Contains(string(jsonBytes), `"failure_category": "ci_failure"`) {
		t.Fatalf("JSON report missing failure category:\n%s", jsonBytes)
	}

	markdownBytes, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatalf("read Markdown report: %v", err)
	}
	if !strings.Contains(string(markdownBytes), "# Reliability Benchmark Report") {
		t.Fatalf("Markdown report missing heading:\n%s", markdownBytes)
	}
}
