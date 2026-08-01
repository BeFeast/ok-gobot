package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/config"
	"ok-gobot/internal/evidence"
	"ok-gobot/internal/reliability"
	"ok-gobot/internal/storage"
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

	cmd := newBenchmarkCommand(nil)
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

func TestBenchmarkReliabilityGitHubProviderUsesSessionEvidence(t *testing.T) {
	t.Parallel()

	store := &fakeBenchmarkStore{
		sessions: map[string]storage.SessionV2{
			"session-cli": {SessionKey: "session-cli", CreatedAt: "2026-05-01 12:00:00", UpdatedAt: "2026-05-01 12:10:00"},
		},
		evidence: map[string][]evidence.Event{
			"session-cli": {
				{SessionKey: "session-cli", Type: evidence.EventWorkspace, Status: "passed", Payload: map[string]any{"branch": "feat/session-cli"}},
				{SessionKey: "session-cli", Type: evidence.EventPullRequest, Status: "passed", Payload: map[string]any{"pr_number": 55, "issue_number": 365}},
			},
		},
	}
	client := &fakeBenchmarkGitHubClient{prs: map[int]reliability.GitHubPullRequest{
		55: {
			Number:                 55,
			URL:                    "https://github.com/BeFeast/ok-gobot/pull/55",
			State:                  "MERGED",
			Checks:                 []reliability.GitHubCheck{{Name: "test", Status: "COMPLETED", Conclusion: "SUCCESS"}},
			ClosingIssueReferences: []reliability.GitHubIssueReference{{Number: 365, URL: "https://github.com/BeFeast/ok-gobot/issues/365"}},
		},
	}}
	cmd := newBenchmarkCommandWithDeps(&config.Config{StoragePath: "unused.db", Maestro: config.MaestroConfig{Repo: "BeFeast/ok-gobot"}}, reliabilityBenchmarkDeps{
		openStore:       func(string) (reliabilitySessionStore, error) { return store, nil },
		newGitHubClient: func(string) reliability.GitHubClient { return client },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"reliability", "--provider", "github", "--session", "session-cli", "--format", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, want := range []string{`"provider": "github"`, `"outcome": "merge_ready"`, `"data_source": "github:BeFeast/ok-gobot session:session-cli"`, `"evidence_links"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
	if !client.authChecked {
		t.Fatal("expected GitHub auth check")
	}
}

func TestBenchmarkReliabilityGitHubProviderRequiresSessionStateBeforeAuth(t *testing.T) {
	t.Parallel()

	client := &fakeBenchmarkGitHubClient{}
	cmd := newBenchmarkCommandWithDeps(&config.Config{StoragePath: "unused.db"}, reliabilityBenchmarkDeps{
		openStore: func(string) (reliabilitySessionStore, error) {
			return &fakeBenchmarkStore{sessions: map[string]storage.SessionV2{}, evidence: map[string][]evidence.Event{}}, nil
		},
		newGitHubClient: func(string) reliability.GitHubClient { return client },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"reliability", "--provider", "github", "--session", "missing"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), `session "missing" not found`) {
		t.Fatalf("expected missing session error, got %v", err)
	}
	if client.authChecked {
		t.Fatal("auth check should not run after missing local session state")
	}
}

func TestBenchmarkReliabilityGitHubProviderRequiresAuth(t *testing.T) {
	t.Parallel()

	store := &fakeBenchmarkStore{
		sessions: map[string]storage.SessionV2{"session-auth": {SessionKey: "session-auth"}},
		evidence: map[string][]evidence.Event{"session-auth": {{SessionKey: "session-auth", Type: evidence.EventPreflight, Status: "passed"}}},
	}
	client := &fakeBenchmarkGitHubClient{authErr: fmt.Errorf("GitHub authentication is required for provider %q; run `gh auth login`", reliability.ProviderGitHub)}
	cmd := newBenchmarkCommandWithDeps(&config.Config{StoragePath: "unused.db"}, reliabilityBenchmarkDeps{
		openStore:       func(string) (reliabilitySessionStore, error) { return store, nil },
		newGitHubClient: func(string) reliability.GitHubClient { return client },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"reliability", "--provider", "github", "--session", "session-auth"})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "GitHub authentication is required") {
		t.Fatalf("expected auth error, got %v", err)
	}
	if !client.authChecked {
		t.Fatal("expected auth check")
	}
}

type fakeBenchmarkStore struct {
	sessions map[string]storage.SessionV2
	evidence map[string][]evidence.Event
}

func (f *fakeBenchmarkStore) Close() error { return nil }

func (f *fakeBenchmarkStore) ListSessionsV2(limit int) ([]storage.SessionV2, error) {
	sessions := make([]storage.SessionV2, 0, len(f.sessions))
	for _, session := range f.sessions {
		sessions = append(sessions, session)
		if limit > 0 && len(sessions) >= limit {
			break
		}
	}
	return sessions, nil
}

func (f *fakeBenchmarkStore) GetSessionV2(sessionKey string) (*storage.SessionV2, error) {
	session, ok := f.sessions[sessionKey]
	if !ok {
		return nil, nil
	}
	return &session, nil
}

func (f *fakeBenchmarkStore) ListEvidenceEvents(sessionKey string, limit int) ([]evidence.Event, error) {
	events := append([]evidence.Event(nil), f.evidence[sessionKey]...)
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

type fakeBenchmarkGitHubClient struct {
	authErr     error
	authChecked bool
	prs         map[int]reliability.GitHubPullRequest
}

func (f *fakeBenchmarkGitHubClient) CheckAuth(context.Context) error {
	f.authChecked = true
	return f.authErr
}

func (f *fakeBenchmarkGitHubClient) PullRequest(_ context.Context, _ string, number int) (reliability.GitHubPullRequest, error) {
	pr, ok := f.prs[number]
	if !ok {
		return reliability.GitHubPullRequest{}, nil
	}
	return pr, nil
}

func (f *fakeBenchmarkGitHubClient) FindPullRequest(_ context.Context, _ string, branch string, issueNumber, _ int) (*reliability.GitHubPullRequest, error) {
	for _, pr := range f.prs {
		if branch != "" && pr.HeadRefName == branch {
			matched := pr
			return &matched, nil
		}
		for _, issue := range pr.ClosingIssueReferences {
			if issue.Number == issueNumber {
				matched := pr
				return &matched, nil
			}
		}
	}
	return nil, nil
}
