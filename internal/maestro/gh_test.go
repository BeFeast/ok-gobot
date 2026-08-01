package maestro

import (
	"strings"
	"testing"

	"ok-gobot/internal/prhygiene"
)

func TestParsePullRequestsMapsGitHubStatusMetadata(t *testing.T) {
	jsonBody := `[
		{
			"number": 242,
			"title": "feat: implement Verification Gate and Paranoid Protocol (#195)",
			"state": "OPEN",
			"url": "https://github.com/BeFeast/ok-gobot/pull/242",
			"isDraft": false,
			"createdAt": "2026-03-25T13:49:32Z",
			"updatedAt": "2026-03-25T13:56:42Z",
			"mergeStateStatus": "DIRTY",
			"reviewDecision": "",
			"statusCheckRollup": [
				{"__typename":"CheckRun","name":"Greptile Review","workflowName":"","status":"COMPLETED","conclusion":"SUCCESS"},
				{"__typename":"StatusContext","context":"ci/test","state":"FAILURE"}
			],
			"latestReviews": [
				{"author":{"login":"reviewer"},"state":"CHANGES_REQUESTED","body":"Please address this."}
			],
			"comments": [
				{"author":{"login":"greptile-apps"},"body":"1 finding requires attention"}
			]
		}
	]`

	prs, err := parsePullRequests([]byte(jsonBody))
	if err != nil {
		t.Fatalf("parsePullRequests failed: %v", err)
	}
	if len(prs) != 1 {
		t.Fatalf("PR count = %d, want 1", len(prs))
	}
	pr := prs[0]
	if pr.Number != 242 || pr.MergeState != "DIRTY" || pr.URL == "" || pr.UpdatedAt.IsZero() {
		t.Fatalf("unexpected PR metadata: %+v", pr)
	}
	if len(pr.Checks) != 2 || pr.Checks[1].Name != "ci/test" || pr.Checks[1].Conclusion != "FAILURE" {
		t.Fatalf("checks not mapped: %+v", pr.Checks)
	}
	if len(pr.Reviews) != 1 || pr.Reviews[0].State != "CHANGES_REQUESTED" {
		t.Fatalf("reviews not mapped: %+v", pr.Reviews)
	}
	if len(pr.Comments) != 1 || !strings.Contains(pr.Comments[0].Body, "requires attention") {
		t.Fatalf("comments not mapped: %+v", pr.Comments)
	}

	blocker, ok := prhygiene.Diagnose(pr, prhygiene.Options{})
	if !ok || blocker.Kind != prhygiene.KindGreptile {
		t.Fatalf("blocker = %+v, want Greptile blocker", blocker)
	}
}
