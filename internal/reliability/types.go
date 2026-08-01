package reliability

import "time"

// ProviderFake is the local, deterministic scenario evaluator used by tests and
// by the default benchmark manifest.
const ProviderFake = "fake"

// ProviderGitHub reads local Maestro evidence and GitHub PR state without
// mutating either source.
const ProviderGitHub = "github"

// LifecycleState names the autonomous PR lifecycle gates tracked by the harness.
type LifecycleState string

const (
	StateIssueSelected     LifecycleState = "issue_selected"
	StatePreflightPassed   LifecycleState = "preflight_passed"
	StateBranchCreated     LifecycleState = "branch_created"
	StatePROpened          LifecycleState = "pr_opened"
	StateCIChecked         LifecycleState = "ci_checked"
	StateReviewChecked     LifecycleState = "review_checked"
	StateRetryAttempted    LifecycleState = "retry_attempted"
	StateMergeReadyEmitted LifecycleState = "merge_ready_emitted"
	StateBlockerEmitted    LifecycleState = "blocker_emitted"
)

// RequiredLifecycleStates is the state vocabulary every reliability report uses.
var RequiredLifecycleStates = []LifecycleState{
	StateIssueSelected,
	StatePreflightPassed,
	StateBranchCreated,
	StatePROpened,
	StateCIChecked,
	StateReviewChecked,
	StateRetryAttempted,
	StateMergeReadyEmitted,
	StateBlockerEmitted,
}

// EventStatus records how a lifecycle gate resolved.
type EventStatus string

const (
	EventStatusPassed  EventStatus = "passed"
	EventStatusFailed  EventStatus = "failed"
	EventStatusSkipped EventStatus = "skipped"
	EventStatusInfo    EventStatus = "info"
)

// Outcome is the terminal reliability state for one scenario.
type Outcome string

const (
	OutcomeMergeReady Outcome = "merge_ready"
	OutcomeBlocked    Outcome = "blocked"
	OutcomeSkipped    Outcome = "skipped"
)

// FailureCategory is the top-level bucket used for reliability accounting.
type FailureCategory string

const (
	CategoryNone               FailureCategory = "none"
	CategoryAgentFailure       FailureCategory = "agent_failure"
	CategoryEnvironmentFailure FailureCategory = "environment_failure"
	CategoryCIFailure          FailureCategory = "ci_failure"
	CategoryReviewFailure      FailureCategory = "review_failure"
	CategoryPolicyGatedSkip    FailureCategory = "policy_gated_skip"
)

// Manifest describes a repeatable set of PR lifecycle scenarios.
type Manifest struct {
	Name      string     `json:"name" yaml:"name"`
	Version   int        `json:"version" yaml:"version"`
	Scenarios []Scenario `json:"scenarios" yaml:"scenarios"`
}

// Scenario is intentionally provider-neutral. The fake block is used by the
// local evaluator; GitHub-backed providers can use Repo, IssueRef, PRRef, and
// Metadata later without changing report generation.
type Scenario struct {
	ID          string            `json:"id" yaml:"id"`
	Title       string            `json:"title" yaml:"title"`
	Provider    string            `json:"provider" yaml:"provider"`
	Repo        string            `json:"repo,omitempty" yaml:"repo"`
	IssueRef    string            `json:"issue_ref,omitempty" yaml:"issue_ref"`
	PRRef       string            `json:"pr_ref,omitempty" yaml:"pr_ref"`
	Description string            `json:"description,omitempty" yaml:"description"`
	Labels      []string          `json:"labels,omitempty" yaml:"labels"`
	Metadata    map[string]string `json:"metadata,omitempty" yaml:"metadata"`
	Fake        FakeScenario      `json:"fake,omitempty" yaml:"fake"`
}

// FakeScenario is a deterministic fixture for exercising the harness without
// secrets, live GitHub access, or live LLM calls.
type FakeScenario struct {
	Events          []LifecycleEvent `json:"events" yaml:"events"`
	Outcome         Outcome          `json:"outcome" yaml:"outcome"`
	FailureCategory FailureCategory  `json:"failure_category" yaml:"failure_category"`
	Reason          string           `json:"reason" yaml:"reason"`
	RetryAttempts   int              `json:"retry_attempts" yaml:"retry_attempts"`
}

// LifecycleEvent records one observed lifecycle gate.
type LifecycleEvent struct {
	State  LifecycleState `json:"state" yaml:"state"`
	Status EventStatus    `json:"status" yaml:"status"`
	Detail string         `json:"detail,omitempty" yaml:"detail"`
}

// ScenarioResult is the machine-readable outcome for one benchmark scenario.
type ScenarioResult struct {
	ID              string           `json:"id"`
	Title           string           `json:"title"`
	Provider        string           `json:"provider"`
	Repo            string           `json:"repo,omitempty"`
	IssueRef        string           `json:"issue_ref,omitempty"`
	PRRef           string           `json:"pr_ref,omitempty"`
	DataSource      string           `json:"data_source,omitempty"`
	EvidenceLinks   []EvidenceLink   `json:"evidence_links,omitempty"`
	Outcome         Outcome          `json:"outcome"`
	FailureCategory FailureCategory  `json:"failure_category"`
	Reason          string           `json:"reason"`
	RetryAttempts   int              `json:"retry_attempts"`
	Lifecycle       []LifecycleEvent `json:"lifecycle"`
}

// EvidenceLink points to read-only evidence used to classify a scenario.
type EvidenceLink struct {
	Type  string `json:"type"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Summary aggregates terminal outcomes across the manifest.
type Summary struct {
	Total      int                     `json:"total"`
	Passed     int                     `json:"passed"`
	Failed     int                     `json:"failed"`
	Skipped    int                     `json:"skipped"`
	Categories map[FailureCategory]int `json:"categories"`
}

// Report is the full benchmark artifact emitted as JSON and Markdown.
type Report struct {
	Name        string           `json:"name"`
	Version     int              `json:"version"`
	GeneratedAt time.Time        `json:"generated_at"`
	Summary     Summary          `json:"summary"`
	Results     []ScenarioResult `json:"results"`
}
