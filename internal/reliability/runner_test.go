package reliability

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestFakeManifestSummaryAndLifecycleStates(t *testing.T) {
	t.Parallel()

	manifest, err := LoadManifestFile(fixtureManifestPath(t))
	if err != nil {
		t.Fatalf("LoadManifestFile() error = %v", err)
	}

	runner := NewRunner(nil)
	runner.Clock = func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) }
	report, err := runner.Run(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if report.Summary.Total != 7 || report.Summary.Passed != 1 || report.Summary.Failed != 5 || report.Summary.Skipped != 1 {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
	wantCategories := map[FailureCategory]int{
		CategoryAgentFailure:       2,
		CategoryEnvironmentFailure: 1,
		CategoryCIFailure:          1,
		CategoryReviewFailure:      1,
		CategoryPolicyGatedSkip:    1,
	}
	for category, want := range wantCategories {
		if got := report.Summary.Categories[category]; got != want {
			t.Fatalf("category %s count = %d, want %d", category, got, want)
		}
	}

	seenStates := map[LifecycleState]bool{}
	for _, result := range report.Results {
		for _, event := range result.Lifecycle {
			seenStates[event.State] = true
		}
	}
	for _, state := range RequiredLifecycleStates {
		if !seenStates[state] {
			t.Fatalf("fixture did not exercise lifecycle state %s", state)
		}
	}

	branchOnly := findResult(t, report, "fake-branch-without-pr")
	if branchOnly.FailureCategory != CategoryAgentFailure {
		t.Fatalf("branch-without-pr category = %s, want %s", branchOnly.FailureCategory, CategoryAgentFailure)
	}
	if lifecycleHasState(branchOnly.Lifecycle, StatePROpened) {
		t.Fatalf("branch-without-pr fixture unexpectedly recorded pr_opened")
	}

	retryExhausted := findResult(t, report, "fake-retry-exhausted-pr-open")
	if retryExhausted.RetryAttempts != 3 {
		t.Fatalf("retry attempts = %d, want 3", retryExhausted.RetryAttempts)
	}
}

func TestReportArtifactsContainCountsAndReasons(t *testing.T) {
	t.Parallel()

	manifest, err := LoadManifestFile(fixtureManifestPath(t))
	if err != nil {
		t.Fatalf("LoadManifestFile() error = %v", err)
	}
	runner := NewRunner(nil)
	runner.Clock = func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) }
	report, err := runner.Run(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	compact := report.Compact()
	for _, want := range []string{
		"PASS 1  FAIL 5  SKIP 1",
		"agent_failure=2",
		"environment_failure=1",
		"FAIL fake-ci-failure [ci_failure]: required CI check failed after PR creation",
		"SKIP fake-policy-gated-skip [policy_gated_skip]: issue matched benchmark skip policy",
	} {
		if !strings.Contains(compact, want) {
			t.Fatalf("compact report missing %q:\n%s", want, compact)
		}
	}

	markdown := report.Markdown()
	for _, want := range []string{
		"# Reliability Benchmark Report",
		"| FAIL | `fake-ci-failure` | `ci_failure` | required CI check failed after PR creation |",
		"| SKIP | `fake-policy-gated-skip` | `policy_gated_skip` | issue matched benchmark skip policy |",
	} {
		if !strings.Contains(markdown, want) {
			t.Fatalf("markdown report missing %q:\n%s", want, markdown)
		}
	}

	jsonBytes, err := report.JSON()
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	var decoded Report
	if err := json.Unmarshal(jsonBytes, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v\n%s", err, jsonBytes)
	}
	if decoded.Summary.Failed != 5 || len(decoded.Results) != 7 {
		t.Fatalf("decoded report mismatch: summary=%+v results=%d", decoded.Summary, len(decoded.Results))
	}
	if got := findResult(t, decoded, "fake-review-comments").Reason; got != "blocking review comments were still open" {
		t.Fatalf("decoded reason = %q", got)
	}
}

func TestEvaluatorErrorBecomesEnvironmentFailure(t *testing.T) {
	t.Parallel()

	manifest := Manifest{
		Name: "provider-error",
		Scenarios: []Scenario{{
			ID:       "github-network-down",
			Title:    "GitHub unavailable",
			Provider: "github",
		}},
	}
	runner := NewRunner(map[string]Evaluator{"github": errEvaluator{err: errors.New("api unavailable")}})
	report, err := runner.Run(context.Background(), manifest)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Summary.Failed != 1 || report.Summary.Categories[CategoryEnvironmentFailure] != 1 {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
	result := findResult(t, report, "github-network-down")
	if result.FailureCategory != CategoryEnvironmentFailure || result.Reason != "api unavailable" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestManifestValidationRejectsBadFakeLifecycle(t *testing.T) {
	t.Parallel()

	_, err := ParseManifest([]byte(`
name: bad
scenarios:
  - id: invalid-state
    provider: fake
    fake:
      outcome: blocked
      events:
        - state: unknown_state
          status: passed
`))
	if err == nil {
		t.Fatal("expected invalid state error")
	}
	if !strings.Contains(err.Error(), "invalid state") {
		t.Fatalf("expected invalid state error, got %v", err)
	}
}

type errEvaluator struct {
	err error
}

func (e errEvaluator) Evaluate(context.Context, Scenario) (ScenarioResult, error) {
	return ScenarioResult{}, e.err
}

func fixtureManifestPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "benchmarks", "reliability", "fake-scenarios.yaml")
}

func findResult(t *testing.T, report Report, id string) ScenarioResult {
	t.Helper()
	for _, result := range report.Results {
		if result.ID == id {
			return result
		}
	}
	t.Fatalf("result %q not found", id)
	return ScenarioResult{}
}

func lifecycleHasState(events []LifecycleEvent, state LifecycleState) bool {
	for _, event := range events {
		if event.State == state {
			return true
		}
	}
	return false
}
