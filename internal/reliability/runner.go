package reliability

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Evaluator runs one scenario through a provider-specific backend.
type Evaluator interface {
	Evaluate(ctx context.Context, scenario Scenario) (ScenarioResult, error)
}

// Runner evaluates every scenario in a manifest and aggregates the report.
type Runner struct {
	Evaluators map[string]Evaluator
	Clock      func() time.Time
}

// NewRunner builds a runner with the deterministic fake evaluator installed by
// default. Callers can pass extra evaluators, for example a future GitHub-backed
// provider keyed by "github".
func NewRunner(evaluators map[string]Evaluator) Runner {
	merged := map[string]Evaluator{ProviderFake: FakeEvaluator{}}
	for name, evaluator := range evaluators {
		if strings.TrimSpace(name) == "" || evaluator == nil {
			continue
		}
		merged[name] = evaluator
	}
	return Runner{Evaluators: merged, Clock: time.Now}
}

// Run executes all manifest scenarios. Provider execution errors are recorded as
// environment failures so one broken scenario does not hide the rest of the run.
func (r Runner) Run(ctx context.Context, manifest Manifest) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ValidateManifest(manifest); err != nil {
		return Report{}, err
	}

	clock := r.Clock
	if clock == nil {
		clock = time.Now
	}
	evaluators := r.Evaluators
	if len(evaluators) == 0 {
		defaults := NewRunner(nil)
		evaluators = defaults.Evaluators
	}

	results := make([]ScenarioResult, 0, len(manifest.Scenarios))
	for _, scenario := range manifest.Scenarios {
		provider := normalizedProvider(scenario.Provider)
		evaluator, ok := evaluators[provider]
		if !ok {
			return Report{}, fmt.Errorf("scenario %q: evaluator %q is not registered", scenario.ID, provider)
		}

		result, err := evaluator.Evaluate(ctx, scenario)
		result = normalizeResult(scenario, provider, result, err)
		results = append(results, result)
	}

	report := Report{
		Name:        valueOrDefault(strings.TrimSpace(manifest.Name), "reliability-benchmark"),
		Version:     manifest.Version,
		GeneratedAt: clock().UTC(),
		Results:     results,
	}
	report.Summary = summarize(results)
	return report, nil
}

// FakeEvaluator replays deterministic lifecycle events from the manifest.
type FakeEvaluator struct{}

// Evaluate implements Evaluator.
func (FakeEvaluator) Evaluate(ctx context.Context, scenario Scenario) (ScenarioResult, error) {
	if err := ctx.Err(); err != nil {
		return ScenarioResult{}, err
	}

	fake := scenario.Fake
	outcome := fake.Outcome
	if outcome == "" {
		outcome = inferOutcome(fake.Events)
	}
	if outcome == "" {
		outcome = OutcomeBlocked
	}

	category := fake.FailureCategory
	if category == "" {
		category = inferCategory(outcome, fake.Events)
	}

	reason := strings.TrimSpace(fake.Reason)
	if reason == "" {
		reason = defaultReason(outcome, category)
	}

	return ScenarioResult{
		Outcome:         outcome,
		FailureCategory: category,
		Reason:          reason,
		RetryAttempts:   fake.RetryAttempts,
		Lifecycle:       normalizeEvents(fake.Events),
	}, nil
}

func normalizeResult(scenario Scenario, provider string, result ScenarioResult, evalErr error) ScenarioResult {
	if result.ID == "" {
		result.ID = scenario.ID
	}
	if result.Title == "" {
		result.Title = valueOrDefault(strings.TrimSpace(scenario.Title), scenario.ID)
	}
	if result.Provider == "" {
		result.Provider = provider
	}
	if result.Repo == "" {
		result.Repo = scenario.Repo
	}
	if result.IssueRef == "" {
		result.IssueRef = scenario.IssueRef
	}
	if result.PRRef == "" {
		result.PRRef = scenario.PRRef
	}

	result.Lifecycle = normalizeEvents(result.Lifecycle)
	if evalErr != nil {
		result.Outcome = OutcomeBlocked
		result.FailureCategory = CategoryEnvironmentFailure
		result.Reason = evalErr.Error()
	}
	if result.Outcome == "" {
		result.Outcome = inferOutcome(result.Lifecycle)
		if result.Outcome == "" {
			result.Outcome = OutcomeBlocked
		}
	}
	if result.FailureCategory == "" {
		result.FailureCategory = inferCategory(result.Outcome, result.Lifecycle)
	}
	if result.Reason == "" {
		result.Reason = defaultReason(result.Outcome, result.FailureCategory)
	}
	return result
}

func normalizeEvents(events []LifecycleEvent) []LifecycleEvent {
	normalized := make([]LifecycleEvent, 0, len(events))
	for _, event := range events {
		if event.Status == "" {
			event.Status = EventStatusPassed
		}
		normalized = append(normalized, event)
	}
	return normalized
}

func inferOutcome(events []LifecycleEvent) Outcome {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].State {
		case StateMergeReadyEmitted:
			return OutcomeMergeReady
		case StateBlockerEmitted:
			if events[i].Status == EventStatusSkipped {
				return OutcomeSkipped
			}
			return OutcomeBlocked
		}
	}
	return ""
}

func inferCategory(outcome Outcome, events []LifecycleEvent) FailureCategory {
	switch outcome {
	case OutcomeMergeReady:
		return CategoryNone
	case OutcomeSkipped:
		return CategoryPolicyGatedSkip
	}
	for _, event := range events {
		if event.Status != EventStatusFailed {
			continue
		}
		switch event.State {
		case StateCIChecked:
			return CategoryCIFailure
		case StateReviewChecked:
			return CategoryReviewFailure
		}
	}
	return CategoryAgentFailure
}

func defaultReason(outcome Outcome, category FailureCategory) string {
	switch outcome {
	case OutcomeMergeReady:
		return "all lifecycle gates passed"
	case OutcomeSkipped:
		return "scenario skipped by policy gate"
	}
	if category == "" || category == CategoryNone {
		category = CategoryAgentFailure
	}
	return fmt.Sprintf("scenario blocked with %s", category)
}

func summarize(results []ScenarioResult) Summary {
	summary := Summary{Total: len(results), Categories: make(map[FailureCategory]int)}
	for _, result := range results {
		switch result.Outcome {
		case OutcomeMergeReady:
			summary.Passed++
		case OutcomeSkipped:
			summary.Skipped++
		default:
			summary.Failed++
		}
		if result.FailureCategory != "" && result.FailureCategory != CategoryNone {
			summary.Categories[result.FailureCategory]++
		}
	}
	return summary
}

func valueOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
