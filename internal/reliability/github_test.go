package reliability

import (
	"context"
	"strings"
	"testing"

	"ok-gobot/internal/evidence"
)

func TestGitHubEvaluatorClassifiesRealLifecycleEvidence(t *testing.T) {
	t.Parallel()

	evidenceSource := fakeGitHubEvidenceSource{
		"session-merged": {
			workspaceEvidence("session-merged", "feat/merged"),
			pullRequestEvidence("session-merged", 101, 365),
		},
		"session-ci": {
			workspaceEvidence("session-ci", "feat/ci"),
			pullRequestEvidence("session-ci", 102, 366),
		},
		"session-review": {
			workspaceEvidence("session-review", "feat/review"),
			pullRequestEvidence("session-review", 103, 367),
		},
		"session-no-pr": {
			workspaceEvidence("session-no-pr", "feat/no-pr"),
			{
				SessionKey: "session-no-pr",
				Type:       evidence.EventPreflight,
				Status:     "passed",
				Payload:    map[string]any{"issue_number": 368},
			},
		},
		"session-policy": {
			{
				SessionKey: "session-policy",
				Type:       evidence.EventPreflight,
				Status:     "skipped",
				Summary:    "policy gate skipped this issue",
				Payload:    map[string]any{"issue_number": 369, "reason": "policy-gated skip"},
			},
		},
	}
	client := &fakeReliabilityGitHubClient{prs: map[int]GitHubPullRequest{
		101: {
			Number:                 101,
			URL:                    "https://github.com/BeFeast/ok-gobot/pull/101",
			State:                  "MERGED",
			Checks:                 []GitHubCheck{{Name: "test", Status: "COMPLETED", Conclusion: "SUCCESS"}},
			Reviews:                []GitHubReview{{Author: "reviewer", State: "APPROVED"}},
			ClosingIssueReferences: []GitHubIssueReference{{Number: 365, URL: "https://github.com/BeFeast/ok-gobot/issues/365"}},
		},
		102: {
			Number:                 102,
			URL:                    "https://github.com/BeFeast/ok-gobot/pull/102",
			State:                  "OPEN",
			Checks:                 []GitHubCheck{{Name: "go test", Status: "COMPLETED", Conclusion: "FAILURE"}},
			ClosingIssueReferences: []GitHubIssueReference{{Number: 366, URL: "https://github.com/BeFeast/ok-gobot/issues/366"}},
		},
		103: {
			Number:                 103,
			URL:                    "https://github.com/BeFeast/ok-gobot/pull/103",
			State:                  "OPEN",
			ReviewDecision:         "CHANGES_REQUESTED",
			Checks:                 []GitHubCheck{{Name: "go test", Status: "COMPLETED", Conclusion: "SUCCESS"}},
			Reviews:                []GitHubReview{{Author: "reviewer", State: "CHANGES_REQUESTED"}},
			ClosingIssueReferences: []GitHubIssueReference{{Number: 367, URL: "https://github.com/BeFeast/ok-gobot/issues/367"}},
		},
	}}

	manifest := Manifest{Name: "github-fixtures", Version: 1}
	for _, sessionKey := range []string{"session-merged", "session-ci", "session-review", "session-no-pr", "session-policy"} {
		manifest.Scenarios = append(manifest.Scenarios, Scenario{
			ID:       sessionKey,
			Provider: ProviderGitHub,
			Repo:     "BeFeast/ok-gobot",
			Metadata: map[string]string{"session_key": sessionKey},
		})
	}

	report, err := NewRunner(map[string]Evaluator{ProviderGitHub: GitHubEvaluator{Client: client, Evidence: evidenceSource}}).Run(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	assertGitHubResult(t, report, "session-merged", OutcomeMergeReady, CategoryNone)
	assertGitHubResult(t, report, "session-ci", OutcomeBlocked, CategoryCIFailure)
	assertGitHubResult(t, report, "session-review", OutcomeBlocked, CategoryReviewFailure)
	assertGitHubResult(t, report, "session-no-pr", OutcomeBlocked, CategoryAgentFailure)
	assertGitHubResult(t, report, "session-policy", OutcomeSkipped, CategoryPolicyGatedSkip)

	merged := findResult(t, report, "session-merged")
	if !strings.Contains(merged.DataSource, "github:BeFeast/ok-gobot") || !strings.Contains(merged.DataSource, "session:session-merged") {
		t.Fatalf("unexpected data source: %q", merged.DataSource)
	}
	for _, linkType := range []string{"issue", "pr", "checks", "reviews", "session_evidence"} {
		if !hasEvidenceLink(merged, linkType) {
			t.Fatalf("merged result missing evidence link %q: %+v", linkType, merged.EvidenceLinks)
		}
	}
	markdown := report.Markdown()
	if !strings.Contains(markdown, "[PR](https://github.com/BeFeast/ok-gobot/pull/101)") || !strings.Contains(markdown, "github:BeFeast/ok-gobot session:session-merged") {
		t.Fatalf("markdown missing GitHub evidence/source:\n%s", markdown)
	}
}

func TestGitHubEvaluatorRequiresSessionEvidence(t *testing.T) {
	t.Parallel()

	_, err := GitHubEvaluator{
		Client:   &fakeReliabilityGitHubClient{},
		Evidence: fakeGitHubEvidenceSource{},
	}.Evaluate(context.Background(), Scenario{
		ID:       "missing",
		Provider: ProviderGitHub,
		Metadata: map[string]string{"session_key": "missing-session"},
	})
	if err == nil || !strings.Contains(err.Error(), "no evidence ledger entries") {
		t.Fatalf("expected actionable missing evidence error, got %v", err)
	}
}

type fakeGitHubEvidenceSource map[string][]evidence.Event

func (f fakeGitHubEvidenceSource) ListEvidenceEvents(sessionKey string, limit int) ([]evidence.Event, error) {
	events := append([]evidence.Event(nil), f[sessionKey]...)
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

type fakeReliabilityGitHubClient struct {
	prs map[int]GitHubPullRequest
}

func (f *fakeReliabilityGitHubClient) CheckAuth(context.Context) error { return nil }

func (f *fakeReliabilityGitHubClient) PullRequest(_ context.Context, _ string, number int) (GitHubPullRequest, error) {
	pr, ok := f.prs[number]
	if !ok {
		return GitHubPullRequest{}, nil
	}
	return pr, nil
}

func (f *fakeReliabilityGitHubClient) FindPullRequest(_ context.Context, _ string, branch string, issueNumber, _ int) (*GitHubPullRequest, error) {
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

func workspaceEvidence(sessionKey, branch string) evidence.Event {
	return evidence.Event{
		SessionKey: sessionKey,
		Type:       evidence.EventWorkspace,
		Status:     "passed",
		Payload:    map[string]any{"branch": branch},
	}
}

func pullRequestEvidence(sessionKey string, prNumber, issueNumber int) evidence.Event {
	return evidence.Event{
		SessionKey: sessionKey,
		Type:       evidence.EventPullRequest,
		Status:     "passed",
		Payload: map[string]any{
			"pr_number":    prNumber,
			"issue_number": issueNumber,
		},
	}
}

func assertGitHubResult(t *testing.T, report Report, id string, outcome Outcome, category FailureCategory) {
	t.Helper()
	result := findResult(t, report, id)
	if result.Outcome != outcome || result.FailureCategory != category {
		t.Fatalf("%s = outcome %s category %s, want %s/%s: %+v", id, result.Outcome, result.FailureCategory, outcome, category, result)
	}
	if result.DataSource == "" {
		t.Fatalf("%s missing data source", id)
	}
}

func hasEvidenceLink(result ScenarioResult, linkType string) bool {
	for _, link := range result.EvidenceLinks {
		if link.Type == linkType && link.URL != "" {
			return true
		}
	}
	return false
}
