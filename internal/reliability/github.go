package reliability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ok-gobot/internal/evidence"
)

const (
	defaultGitHubEvidenceLimit = 200
	defaultGitHubPRSearchLimit = 100
	githubPRJSONFields         = "number,title,url,state,isDraft,mergeStateStatus,headRefName,baseRefName,reviewDecision,statusCheckRollup,reviews,closingIssuesReferences"
	defaultGHCLIMaxAttempts    = 3
	defaultGHCLIBaseBackoff    = 200 * time.Millisecond
)

// GitHubClient is the read-only GitHub surface used by the reliability provider.
type GitHubClient interface {
	CheckAuth(ctx context.Context) error
	PullRequest(ctx context.Context, repo string, number int) (GitHubPullRequest, error)
	FindPullRequest(ctx context.Context, repo, branch string, issueNumber, limit int) (*GitHubPullRequest, error)
}

// EvidenceSource reads the local Maestro evidence ledger for one session.
type EvidenceSource interface {
	ListEvidenceEvents(sessionKey string, limit int) ([]evidence.Event, error)
}

// GitHubPullRequest is a normalized, read-only PR lifecycle snapshot.
type GitHubPullRequest struct {
	Number                 int
	Title                  string
	URL                    string
	State                  string
	IsDraft                bool
	MergeStateStatus       string
	HeadRefName            string
	BaseRefName            string
	ReviewDecision         string
	Checks                 []GitHubCheck
	Reviews                []GitHubReview
	ClosingIssueReferences []GitHubIssueReference
}

// GitHubCheck is one normalized status check or check run.
type GitHubCheck struct {
	Name       string
	Status     string
	Conclusion string
	DetailsURL string
}

// GitHubReview is one normalized PR review.
type GitHubReview struct {
	Author string
	State  string
	URL    string
}

// GitHubIssueReference is an issue linked to a PR.
type GitHubIssueReference struct {
	Number int
	URL    string
}

// GHCLIClient reads GitHub state through the gh CLI. It only uses view/list/auth
// commands and never invokes mutating GitHub operations.
type GHCLIClient struct {
	Dir         string
	Binary      string
	MaxAttempts int
	BaseBackoff time.Duration
}

// NewGHCLIClient returns a read-only GitHub CLI client rooted at dir.
func NewGHCLIClient(dir string) *GHCLIClient {
	return &GHCLIClient{
		Dir:         dir,
		Binary:      "gh",
		MaxAttempts: defaultGHCLIMaxAttempts,
		BaseBackoff: defaultGHCLIBaseBackoff,
	}
}

// CheckAuth verifies that gh can read GitHub state before a benchmark starts.
func (c *GHCLIClient) CheckAuth(ctx context.Context) error {
	if _, err := c.run(ctx, "auth", "status", "--hostname", "github.com"); err != nil {
		return fmt.Errorf("GitHub authentication is required for provider %q; run `gh auth login` or set GH_TOKEN/GITHUB_TOKEN: %w", ProviderGitHub, err)
	}
	return nil
}

// PullRequest reads one PR by number.
func (c *GHCLIClient) PullRequest(ctx context.Context, repo string, number int) (GitHubPullRequest, error) {
	if number <= 0 {
		return GitHubPullRequest{}, fmt.Errorf("pull request number is required")
	}
	args := []string{"pr", "view", strconv.Itoa(number), "--json", githubPRJSONFields}
	if strings.TrimSpace(repo) != "" {
		args = append(args, "--repo", strings.TrimSpace(repo))
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return GitHubPullRequest{}, err
	}
	var raw ghRawPullRequest
	if err := json.Unmarshal(out, &raw); err != nil {
		return GitHubPullRequest{}, fmt.Errorf("parse gh pr view: %w", err)
	}
	return normalizeRawPullRequest(raw), nil
}

// FindPullRequest looks for a PR by branch first, then by linked issue among
// recent PRs. It returns nil when no matching PR exists.
func (c *GHCLIClient) FindPullRequest(ctx context.Context, repo, branch string, issueNumber, limit int) (*GitHubPullRequest, error) {
	if limit <= 0 {
		limit = defaultGitHubPRSearchLimit
	}
	branch = strings.TrimSpace(branch)
	var branchErr error
	if branch != "" {
		prs, err := c.listPullRequests(ctx, repo, limit, "--head", branch)
		if err != nil {
			branchErr = err
		} else if len(prs) > 0 {
			return &prs[0], nil
		}
	}

	if issueNumber > 0 {
		prs, err := c.listPullRequests(ctx, repo, limit)
		if err != nil {
			if branchErr != nil {
				return nil, branchErr
			}
			return nil, err
		}
		for _, pr := range prs {
			for _, issue := range pr.ClosingIssueReferences {
				if issue.Number == issueNumber {
					matched := pr
					return &matched, nil
				}
			}
		}
	}

	if branchErr != nil && issueNumber <= 0 {
		return nil, branchErr
	}
	return nil, nil
}

func (c *GHCLIClient) listPullRequests(ctx context.Context, repo string, limit int, extraArgs ...string) ([]GitHubPullRequest, error) {
	if limit <= 0 {
		limit = defaultGitHubPRSearchLimit
	}
	args := []string{"pr", "list", "--state", "all", "--limit", strconv.Itoa(limit), "--json", githubPRJSONFields}
	args = append(args, extraArgs...)
	if strings.TrimSpace(repo) != "" {
		args = append(args, "--repo", strings.TrimSpace(repo))
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var raw []ghRawPullRequest
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh pr list: %w", err)
	}
	prs := make([]GitHubPullRequest, 0, len(raw))
	for _, item := range raw {
		prs = append(prs, normalizeRawPullRequest(item))
	}
	return prs, nil
}

func (c *GHCLIClient) run(ctx context.Context, args ...string) ([]byte, error) {
	binary := c.Binary
	if binary == "" {
		binary = "gh"
	}
	attempts := c.MaxAttempts
	if attempts <= 0 {
		attempts = defaultGHCLIMaxAttempts
	}
	backoff := c.BaseBackoff
	if backoff <= 0 {
		backoff = defaultGHCLIBaseBackoff
	}

	var (
		out     []byte
		err     error
		lastMsg string
	)
	for attempt := 1; attempt <= attempts; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		if c.Dir != "" {
			cmd.Dir = c.Dir
		}
		out, err = cmd.CombinedOutput()
		if err == nil {
			return out, nil
		}
		lastMsg = strings.TrimSpace(string(out))
		if attempt == attempts {
			break
		}
		sleep := backoff << (attempt - 1)
		timer := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if lastMsg == "" {
		return nil, err
	}
	return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, lastMsg)
}

type ghRawPullRequest struct {
	Number                 int                   `json:"number"`
	Title                  string                `json:"title"`
	URL                    string                `json:"url"`
	State                  string                `json:"state"`
	IsDraft                bool                  `json:"isDraft"`
	MergeStateStatus       string                `json:"mergeStateStatus"`
	HeadRefName            string                `json:"headRefName"`
	BaseRefName            string                `json:"baseRefName"`
	ReviewDecision         string                `json:"reviewDecision"`
	StatusCheckRollup      []map[string]any      `json:"statusCheckRollup"`
	Reviews                []ghRawReview         `json:"reviews"`
	ClosingIssueReferences []ghRawIssueReference `json:"closingIssuesReferences"`
}

type ghRawReview struct {
	Author *struct {
		Login string `json:"login"`
	} `json:"author"`
	State string `json:"state"`
	URL   string `json:"url"`
}

type ghRawIssueReference struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

func normalizeRawPullRequest(raw ghRawPullRequest) GitHubPullRequest {
	pr := GitHubPullRequest{
		Number:           raw.Number,
		Title:            strings.TrimSpace(raw.Title),
		URL:              strings.TrimSpace(raw.URL),
		State:            strings.ToUpper(strings.TrimSpace(raw.State)),
		IsDraft:          raw.IsDraft,
		MergeStateStatus: strings.ToUpper(strings.TrimSpace(raw.MergeStateStatus)),
		HeadRefName:      strings.TrimSpace(raw.HeadRefName),
		BaseRefName:      strings.TrimSpace(raw.BaseRefName),
		ReviewDecision:   strings.ToUpper(strings.TrimSpace(raw.ReviewDecision)),
	}
	for _, item := range raw.StatusCheckRollup {
		check := GitHubCheck{
			Name:       firstMapString(item, "name", "context"),
			Status:     strings.ToUpper(firstMapString(item, "status", "state")),
			Conclusion: strings.ToUpper(firstMapString(item, "conclusion")),
			DetailsURL: firstMapString(item, "detailsUrl", "targetUrl", "url"),
		}
		if check.Name != "" || check.Status != "" || check.Conclusion != "" {
			pr.Checks = append(pr.Checks, check)
		}
	}
	for _, rawReview := range raw.Reviews {
		review := GitHubReview{State: strings.ToUpper(strings.TrimSpace(rawReview.State)), URL: strings.TrimSpace(rawReview.URL)}
		if rawReview.Author != nil {
			review.Author = strings.TrimSpace(rawReview.Author.Login)
		}
		if review.State != "" || review.Author != "" || review.URL != "" {
			pr.Reviews = append(pr.Reviews, review)
		}
	}
	for _, issue := range raw.ClosingIssueReferences {
		if issue.Number <= 0 {
			continue
		}
		pr.ClosingIssueReferences = append(pr.ClosingIssueReferences, GitHubIssueReference{Number: issue.Number, URL: strings.TrimSpace(issue.URL)})
	}
	return pr
}

func firstMapString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := item[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

// GitHubEvaluator scores one session by combining local evidence with read-only
// GitHub PR state.
type GitHubEvaluator struct {
	Client        GitHubClient
	Evidence      EvidenceSource
	EvidenceLimit int
	PRSearchLimit int
}

// Evaluate implements Evaluator.
func (e GitHubEvaluator) Evaluate(ctx context.Context, scenario Scenario) (ScenarioResult, error) {
	if err := ctx.Err(); err != nil {
		return ScenarioResult{}, err
	}
	if e.Client == nil {
		return ScenarioResult{}, fmt.Errorf("github evaluator requires a GitHub client")
	}
	if e.Evidence == nil {
		return ScenarioResult{}, fmt.Errorf("github evaluator requires a session evidence source")
	}

	sessionKey := strings.TrimSpace(scenario.Metadata["session_key"])
	if sessionKey == "" {
		return ScenarioResult{}, fmt.Errorf("scenario %q: metadata.session_key is required for provider %q", scenario.ID, ProviderGitHub)
	}

	limit := e.EvidenceLimit
	if limit <= 0 {
		limit = defaultGitHubEvidenceLimit
	}
	events, err := e.Evidence.ListEvidenceEvents(sessionKey, limit)
	if err != nil {
		return ScenarioResult{}, fmt.Errorf("read evidence for session %q: %w", sessionKey, err)
	}
	if len(events) == 0 {
		return ScenarioResult{}, fmt.Errorf("session %q has no evidence ledger entries; choose a session with Maestro evidence or run `ok-gobot sessions evidence %s` to inspect it", sessionKey, sessionKey)
	}

	snapshot := extractGitHubSessionEvidence(scenario, sessionKey, events)
	var pr *GitHubPullRequest
	if !snapshot.PolicyGated {
		if snapshot.PRNumber > 0 {
			loaded, err := e.Client.PullRequest(ctx, snapshot.Repo, snapshot.PRNumber)
			if err != nil {
				return ScenarioResult{}, fmt.Errorf("read PR %s: %w", refWithRepo(snapshot.Repo, snapshot.PRNumber), err)
			}
			pr = &loaded
		} else {
			searchLimit := e.PRSearchLimit
			if searchLimit <= 0 {
				searchLimit = defaultGitHubPRSearchLimit
			}
			found, err := e.Client.FindPullRequest(ctx, snapshot.Repo, snapshot.Branch, snapshot.IssueNumber, searchLimit)
			if err != nil {
				return ScenarioResult{}, fmt.Errorf("search PR for session %q: %w", sessionKey, err)
			}
			pr = found
		}
	}
	if pr != nil {
		applyPRSnapshot(&snapshot, *pr)
	}

	result := classifyGitHubSession(snapshot, pr)
	result.Repo = snapshot.Repo
	result.IssueRef = issueRef(snapshot)
	result.PRRef = prRef(snapshot)
	result.DataSource = githubDataSource(snapshot)
	result.EvidenceLinks = githubEvidenceLinks(snapshot, pr)
	return result, nil
}

type githubSessionSnapshot struct {
	SessionKey    string
	Repo          string
	IssueNumber   int
	IssueURL      string
	PRNumber      int
	PRURL         string
	Branch        string
	JobID         string
	RetryAttempts int
	PolicyGated   bool
	PolicyReason  string
	FinalOutcome  string
	FinalBlocker  string
	FinalReason   string
}

func extractGitHubSessionEvidence(scenario Scenario, sessionKey string, events []evidence.Event) githubSessionSnapshot {
	snapshot := githubSessionSnapshot{SessionKey: sessionKey, Repo: strings.TrimSpace(scenario.Repo)}
	if repo, number, link, ok := parseRepoNumberRef(scenario.IssueRef, "issues"); ok {
		setRepoIfEmpty(&snapshot, repo)
		snapshot.IssueNumber = number
		snapshot.IssueURL = link
	}
	if repo, number, link, ok := parseRepoNumberRef(scenario.PRRef, "pull"); ok {
		setRepoIfEmpty(&snapshot, repo)
		snapshot.PRNumber = number
		snapshot.PRURL = link
	}
	applyStringMetadata(scenario.Metadata, "repo", &snapshot.Repo)
	applyStringMetadata(scenario.Metadata, "branch", &snapshot.Branch)
	if n := numberFromString(scenario.Metadata["issue_number"]); n > 0 {
		snapshot.IssueNumber = n
	}
	if n := numberFromString(scenario.Metadata["pr_number"]); n > 0 {
		snapshot.PRNumber = n
	}
	if n := numberFromString(scenario.Metadata["retry_attempts"]); n >= 0 {
		snapshot.RetryAttempts = n
	}
	applyStringMetadata(scenario.Metadata, "job_id", &snapshot.JobID)

	retryEvents := 0
	maxAttempt := 0
	for _, event := range events {
		if snapshot.JobID == "" {
			snapshot.JobID = strings.TrimSpace(event.JobID)
		}
		payload := event.Payload
		setRepoIfEmpty(&snapshot, firstPayloadString(payload, "repo", "repository", "github_repo"))
		if repo, number, link, ok := parseRepoNumberRef(firstPayloadString(payload, "issue_ref"), "issues"); ok {
			setRepoIfEmpty(&snapshot, repo)
			if snapshot.IssueNumber == 0 {
				snapshot.IssueNumber = number
			}
			if snapshot.IssueURL == "" {
				snapshot.IssueURL = link
			}
		}
		if repo, number, link, ok := parseRepoNumberRef(firstPayloadString(payload, "issue_url"), "issues"); ok {
			setRepoIfEmpty(&snapshot, repo)
			if snapshot.IssueNumber == 0 {
				snapshot.IssueNumber = number
			}
			if snapshot.IssueURL == "" {
				snapshot.IssueURL = link
			}
		}
		if n := firstPayloadInt(payload, "issue_number", "issue"); n > 0 && snapshot.IssueNumber == 0 {
			snapshot.IssueNumber = n
		}
		if branch := firstPayloadString(payload, "branch", "head_ref", "head_ref_name", "headRefName", "source_branch"); branch != "" && snapshot.Branch == "" {
			snapshot.Branch = strings.TrimPrefix(branch, "refs/heads/")
		}

		if event.Type == evidence.EventPullRequest {
			if repo, number, link, ok := parseRepoNumberRef(firstPayloadString(payload, "pr_ref"), "pull"); ok {
				setRepoIfEmpty(&snapshot, repo)
				if snapshot.PRNumber == 0 {
					snapshot.PRNumber = number
				}
				if snapshot.PRURL == "" {
					snapshot.PRURL = link
				}
			}
			if repo, number, link, ok := parseRepoNumberRef(firstPayloadString(payload, "url", "pr_url", "pull_request_url", "html_url"), "pull"); ok {
				setRepoIfEmpty(&snapshot, repo)
				if snapshot.PRNumber == 0 {
					snapshot.PRNumber = number
				}
				if snapshot.PRURL == "" {
					snapshot.PRURL = link
				}
			}
			if n := firstPayloadInt(payload, "pr_number", "pull_request_number", "number"); n > 0 && snapshot.PRNumber == 0 {
				snapshot.PRNumber = n
			}
		}

		if event.Type == evidence.EventRetryDecision {
			retryEvents++
		}
		if attempt := firstPayloadInt(payload, "attempt"); attempt > maxAttempt {
			maxAttempt = attempt
		}
		if attempts := firstPayloadInt(payload, "retry_attempts", "retry_count"); attempts > snapshot.RetryAttempts {
			snapshot.RetryAttempts = attempts
		}

		if eventPolicyGated(event) {
			snapshot.PolicyGated = true
			if snapshot.PolicyReason == "" {
				snapshot.PolicyReason = firstNonEmpty(firstPayloadString(payload, "reason", "limit_reason", "policy_reason"), event.Summary)
			}
		}
		if event.Type == evidence.EventFinalDecision {
			snapshot.FinalOutcome = firstPayloadString(payload, "outcome", "status")
			snapshot.FinalBlocker = firstPayloadString(payload, "blocker", "failure_category", "category", "limit_reason")
			snapshot.FinalReason = firstNonEmpty(firstPayloadString(payload, "reason"), event.Summary)
		}
	}
	if retryEvents > snapshot.RetryAttempts {
		snapshot.RetryAttempts = retryEvents
	}
	if maxAttempt > 1 && maxAttempt-1 > snapshot.RetryAttempts {
		snapshot.RetryAttempts = maxAttempt - 1
	}
	return snapshot
}

func classifyGitHubSession(snapshot githubSessionSnapshot, pr *GitHubPullRequest) ScenarioResult {
	if snapshot.PolicyGated {
		reason := strings.TrimSpace(snapshot.PolicyReason)
		if reason == "" {
			reason = "session was skipped by a policy gate"
		}
		return ScenarioResult{
			Outcome:         OutcomeSkipped,
			FailureCategory: CategoryPolicyGatedSkip,
			Reason:          reason,
			RetryAttempts:   snapshot.RetryAttempts,
			Lifecycle:       githubLifecycle(snapshot, nil, checksSkipped, reviewsSkipped, OutcomeSkipped, CategoryPolicyGatedSkip),
		}
	}

	if pr == nil || pr.Number <= 0 {
		reason := "no pull request found for session evidence"
		if snapshot.Branch != "" {
			reason = fmt.Sprintf("branch %s was observed but no pull request was found", snapshot.Branch)
		}
		return ScenarioResult{
			Outcome:         OutcomeBlocked,
			FailureCategory: CategoryAgentFailure,
			Reason:          reason,
			RetryAttempts:   snapshot.RetryAttempts,
			Lifecycle:       githubLifecycle(snapshot, nil, checksSkipped, reviewsSkipped, OutcomeBlocked, CategoryAgentFailure),
		}
	}

	checks := classifyGitHubChecks(*pr)
	reviews := classifyGitHubReviews(*pr)
	state := strings.ToUpper(strings.TrimSpace(pr.State))
	if state == "MERGED" {
		return ScenarioResult{
			Outcome:         OutcomeMergeReady,
			FailureCategory: CategoryNone,
			Reason:          fmt.Sprintf("pull request #%d was merged", pr.Number),
			RetryAttempts:   snapshot.RetryAttempts,
			Lifecycle:       githubLifecycle(snapshot, pr, checks, reviews, OutcomeMergeReady, CategoryNone),
		}
	}
	if state == "CLOSED" {
		return ScenarioResult{
			Outcome:         OutcomeBlocked,
			FailureCategory: CategoryAgentFailure,
			Reason:          fmt.Sprintf("pull request #%d was closed without merge", pr.Number),
			RetryAttempts:   snapshot.RetryAttempts,
			Lifecycle:       githubLifecycle(snapshot, pr, checks, reviews, OutcomeBlocked, CategoryAgentFailure),
		}
	}
	if pr.IsDraft {
		return ScenarioResult{
			Outcome:         OutcomeBlocked,
			FailureCategory: CategoryAgentFailure,
			Reason:          fmt.Sprintf("pull request #%d is still draft", pr.Number),
			RetryAttempts:   snapshot.RetryAttempts,
			Lifecycle:       githubLifecycle(snapshot, pr, checks, reviews, OutcomeBlocked, CategoryAgentFailure),
		}
	}
	if checks == checksFailed {
		return ScenarioResult{
			Outcome:         OutcomeBlocked,
			FailureCategory: CategoryCIFailure,
			Reason:          fmt.Sprintf("pull request #%d has failing checks", pr.Number),
			RetryAttempts:   snapshot.RetryAttempts,
			Lifecycle:       githubLifecycle(snapshot, pr, checks, reviews, OutcomeBlocked, CategoryCIFailure),
		}
	}
	if reviews == reviewsActionable {
		return ScenarioResult{
			Outcome:         OutcomeBlocked,
			FailureCategory: CategoryReviewFailure,
			Reason:          fmt.Sprintf("pull request #%d has blocking review feedback", pr.Number),
			RetryAttempts:   snapshot.RetryAttempts,
			Lifecycle:       githubLifecycle(snapshot, pr, checks, reviews, OutcomeBlocked, CategoryReviewFailure),
		}
	}
	if checks == checksPending || checks == checksUnknown {
		return ScenarioResult{
			Outcome:         OutcomeBlocked,
			FailureCategory: CategoryEnvironmentFailure,
			Reason:          fmt.Sprintf("pull request #%d checks are not complete", pr.Number),
			RetryAttempts:   snapshot.RetryAttempts,
			Lifecycle:       githubLifecycle(snapshot, pr, checks, reviews, OutcomeBlocked, CategoryEnvironmentFailure),
		}
	}
	return ScenarioResult{
		Outcome:         OutcomeMergeReady,
		FailureCategory: CategoryNone,
		Reason:          fmt.Sprintf("pull request #%d is open with passing checks and no blocking review feedback", pr.Number),
		RetryAttempts:   snapshot.RetryAttempts,
		Lifecycle:       githubLifecycle(snapshot, pr, checks, reviews, OutcomeMergeReady, CategoryNone),
	}
}

type checkClassification string

const (
	checksPassed  checkClassification = "passed"
	checksFailed  checkClassification = "failed"
	checksPending checkClassification = "pending"
	checksUnknown checkClassification = "unknown"
	checksSkipped checkClassification = "skipped"
)

type reviewClassification string

const (
	reviewsPassed     reviewClassification = "passed"
	reviewsActionable reviewClassification = "actionable"
	reviewsSkipped    reviewClassification = "skipped"
)

func classifyGitHubChecks(pr GitHubPullRequest) checkClassification {
	if len(pr.Checks) == 0 {
		switch strings.ToUpper(strings.TrimSpace(pr.MergeStateStatus)) {
		case "CLEAN", "HAS_HOOKS", "UNSTABLE":
			return checksPassed
		case "BLOCKED", "DIRTY", "UNKNOWN", "BEHIND":
			return checksUnknown
		default:
			return checksUnknown
		}
	}
	for _, check := range pr.Checks {
		conclusion := strings.ToUpper(strings.TrimSpace(check.Conclusion))
		status := strings.ToUpper(strings.TrimSpace(check.Status))
		switch conclusion {
		case "FAILURE", "FAILED", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED":
			return checksFailed
		}
		switch status {
		case "FAILURE", "FAILED", "ERROR":
			return checksFailed
		case "QUEUED", "PENDING", "IN_PROGRESS", "REQUESTED", "WAITING", "EXPECTED":
			return checksPending
		}
		if conclusion == "" && status == "" {
			return checksUnknown
		}
	}
	return checksPassed
}

func classifyGitHubReviews(pr GitHubPullRequest) reviewClassification {
	switch strings.ToUpper(strings.TrimSpace(pr.ReviewDecision)) {
	case "CHANGES_REQUESTED":
		return reviewsActionable
	case "APPROVED":
		return reviewsPassed
	}
	for _, review := range pr.Reviews {
		if strings.ToUpper(strings.TrimSpace(review.State)) == "CHANGES_REQUESTED" {
			return reviewsActionable
		}
	}
	return reviewsPassed
}

func githubLifecycle(snapshot githubSessionSnapshot, pr *GitHubPullRequest, checks checkClassification, reviews reviewClassification, outcome Outcome, category FailureCategory) []LifecycleEvent {
	events := []LifecycleEvent{{State: StateIssueSelected, Status: EventStatusPassed}}
	if snapshot.IssueNumber == 0 {
		events[0].Status = EventStatusInfo
		events[0].Detail = "issue number not recorded"
	}
	preflight := LifecycleEvent{State: StatePreflightPassed, Status: EventStatusPassed}
	if outcome == OutcomeSkipped {
		preflight.Status = EventStatusSkipped
		preflight.Detail = "policy gate declined execution"
	}
	events = append(events, preflight)

	branch := strings.TrimSpace(snapshot.Branch)
	if pr != nil && branch == "" {
		branch = strings.TrimSpace(pr.HeadRefName)
	}
	branchEvent := LifecycleEvent{State: StateBranchCreated, Status: EventStatusPassed}
	if branch == "" {
		branchEvent.Status = EventStatusSkipped
		branchEvent.Detail = "branch not recorded"
	} else {
		branchEvent.Detail = branch
	}
	events = append(events, branchEvent)

	prEvent := LifecycleEvent{State: StatePROpened, Status: EventStatusPassed}
	if pr == nil || pr.Number <= 0 {
		if outcome == OutcomeSkipped {
			prEvent.Status = EventStatusSkipped
			prEvent.Detail = "policy-gated before PR creation"
		} else {
			prEvent.Status = EventStatusFailed
			prEvent.Detail = "no PR found"
		}
	} else {
		prEvent.Detail = refWithRepo(snapshot.Repo, pr.Number)
	}
	events = append(events, prEvent)

	ciEvent := LifecycleEvent{State: StateCIChecked, Status: EventStatusPassed, Detail: string(checks)}
	switch checks {
	case checksFailed, checksPending, checksUnknown:
		ciEvent.Status = EventStatusFailed
	case checksSkipped:
		ciEvent.Status = EventStatusSkipped
	}
	if pr == nil || pr.Number <= 0 {
		ciEvent.Status = EventStatusSkipped
		ciEvent.Detail = "no PR checks"
	}
	events = append(events, ciEvent)

	reviewEvent := LifecycleEvent{State: StateReviewChecked, Status: EventStatusPassed, Detail: string(reviews)}
	if reviews == reviewsActionable {
		reviewEvent.Status = EventStatusFailed
	}
	if reviews == reviewsSkipped || pr == nil || pr.Number <= 0 || category == CategoryCIFailure {
		reviewEvent.Status = EventStatusSkipped
		if category == CategoryCIFailure {
			reviewEvent.Detail = "not evaluated after CI failure"
		} else if pr == nil || pr.Number <= 0 {
			reviewEvent.Detail = "no PR reviews"
		}
	}
	events = append(events, reviewEvent)

	retryEvent := LifecycleEvent{State: StateRetryAttempted, Status: EventStatusSkipped, Detail: "no retries recorded"}
	if snapshot.RetryAttempts > 0 {
		retryEvent.Status = EventStatusPassed
		retryEvent.Detail = fmt.Sprintf("%d retries recorded", snapshot.RetryAttempts)
	}
	events = append(events, retryEvent)

	if outcome == OutcomeMergeReady {
		events = append(events, LifecycleEvent{State: StateMergeReadyEmitted, Status: EventStatusPassed})
	} else {
		status := EventStatusFailed
		if outcome == OutcomeSkipped {
			status = EventStatusSkipped
		}
		events = append(events, LifecycleEvent{State: StateBlockerEmitted, Status: status, Detail: string(category)})
	}
	return events
}

func applyPRSnapshot(snapshot *githubSessionSnapshot, pr GitHubPullRequest) {
	if snapshot.PRNumber == 0 {
		snapshot.PRNumber = pr.Number
	}
	if snapshot.PRURL == "" {
		snapshot.PRURL = strings.TrimSpace(pr.URL)
	}
	if snapshot.Branch == "" {
		snapshot.Branch = strings.TrimSpace(pr.HeadRefName)
	}
	if repo, _, _, ok := parseRepoNumberRef(pr.URL, "pull"); ok {
		setRepoIfEmpty(snapshot, repo)
	}
	if snapshot.IssueNumber == 0 && len(pr.ClosingIssueReferences) > 0 {
		snapshot.IssueNumber = pr.ClosingIssueReferences[0].Number
		snapshot.IssueURL = pr.ClosingIssueReferences[0].URL
	}
}

func githubDataSource(snapshot githubSessionSnapshot) string {
	parts := []string{ProviderGitHub}
	if snapshot.Repo != "" {
		parts[0] = ProviderGitHub + ":" + snapshot.Repo
	}
	if snapshot.SessionKey != "" {
		parts = append(parts, "session:"+snapshot.SessionKey)
	}
	if snapshot.JobID != "" {
		parts = append(parts, "job:"+snapshot.JobID)
	}
	return strings.Join(parts, " ")
}

func githubEvidenceLinks(snapshot githubSessionSnapshot, pr *GitHubPullRequest) []EvidenceLink {
	repo := snapshot.Repo
	if repo == "" && pr != nil {
		repo, _, _, _ = parseRepoNumberRef(pr.URL, "pull")
	}
	var links []EvidenceLink
	if issueURL := issueURL(snapshot, repo); issueURL != "" {
		links = append(links, EvidenceLink{Type: "issue", Label: "issue", URL: issueURL})
	}
	if prURL := pullRequestURL(snapshot, repo); prURL != "" {
		links = append(links, EvidenceLink{Type: "pr", Label: "PR", URL: prURL})
		links = append(links, EvidenceLink{Type: "checks", Label: "checks", URL: strings.TrimRight(prURL, "/") + "/checks"})
		links = append(links, EvidenceLink{Type: "reviews", Label: "reviews", URL: strings.TrimRight(prURL, "/") + "/files"})
	}
	if snapshot.SessionKey != "" {
		ledger := url.URL{Scheme: "ok-gobot", Host: "sessions", Path: "/evidence"}
		q := ledger.Query()
		q.Set("key", snapshot.SessionKey)
		ledger.RawQuery = q.Encode()
		links = append(links, EvidenceLink{Type: "session_evidence", Label: "session evidence", URL: ledger.String()})
	}
	return links
}

func issueRef(snapshot githubSessionSnapshot) string {
	if snapshot.IssueNumber <= 0 {
		return ""
	}
	return refWithRepo(snapshot.Repo, snapshot.IssueNumber)
}

func prRef(snapshot githubSessionSnapshot) string {
	if snapshot.PRNumber <= 0 {
		return ""
	}
	return refWithRepo(snapshot.Repo, snapshot.PRNumber)
}

func issueURL(snapshot githubSessionSnapshot, repo string) string {
	if snapshot.IssueURL != "" {
		return snapshot.IssueURL
	}
	if repo != "" && snapshot.IssueNumber > 0 {
		return fmt.Sprintf("https://github.com/%s/issues/%d", repo, snapshot.IssueNumber)
	}
	return ""
}

func pullRequestURL(snapshot githubSessionSnapshot, repo string) string {
	if snapshot.PRURL != "" {
		return snapshot.PRURL
	}
	if repo != "" && snapshot.PRNumber > 0 {
		return fmt.Sprintf("https://github.com/%s/pull/%d", repo, snapshot.PRNumber)
	}
	return ""
}

func refWithRepo(repo string, number int) string {
	if number <= 0 {
		return ""
	}
	if strings.TrimSpace(repo) == "" {
		return fmt.Sprintf("#%d", number)
	}
	return fmt.Sprintf("%s#%d", strings.TrimSpace(repo), number)
}

func parseRepoNumberRef(raw, githubPath string) (string, int, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", 0, "", false
	}
	if parsed, err := url.Parse(raw); err == nil && strings.EqualFold(parsed.Host, "github.com") {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) >= 4 && parts[0] != "" && parts[1] != "" && parts[2] == githubPath {
			number := numberFromString(parts[3])
			if number > 0 {
				return parts[0] + "/" + parts[1], number, parsed.String(), true
			}
		}
	}
	if strings.HasPrefix(raw, "#") {
		if number := numberFromString(strings.TrimPrefix(raw, "#")); number > 0 {
			return "", number, "", true
		}
	}
	if idx := strings.LastIndex(raw, "#"); idx >= 0 && idx < len(raw)-1 {
		number := numberFromString(raw[idx+1:])
		repo := strings.TrimSpace(raw[:idx])
		if number > 0 && strings.Count(repo, "/") == 1 {
			return repo, number, "", true
		}
	}
	return "", 0, "", false
}

func numberFromString(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	raw = strings.TrimPrefix(raw, "#")
	for i, r := range raw {
		if r < '0' || r > '9' {
			if i == 0 {
				return 0
			}
			raw = raw[:i]
			break
		}
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

func applyStringMetadata(metadata map[string]string, key string, target *string) {
	if target == nil || strings.TrimSpace(*target) != "" {
		return
	}
	*target = strings.TrimSpace(metadata[key])
}

func setRepoIfEmpty(snapshot *githubSessionSnapshot, repo string) {
	if strings.TrimSpace(snapshot.Repo) == "" {
		snapshot.Repo = strings.TrimSpace(repo)
	}
}

func firstPayloadString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		text := strings.TrimSpace(fmt.Sprint(value))
		if text != "" && text != "<nil>" {
			return text
		}
	}
	return ""
}

func firstPayloadInt(payload map[string]any, keys ...string) int {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok || value == nil {
			continue
		}
		switch v := value.(type) {
		case int:
			return v
		case int64:
			return int(v)
		case float64:
			return int(v)
		case json.Number:
			if n, err := v.Int64(); err == nil {
				return int(n)
			}
		case string:
			if n := numberFromString(v); n > 0 {
				return n
			}
		default:
			if n := numberFromString(fmt.Sprint(v)); n > 0 {
				return n
			}
		}
	}
	return 0
}

func eventPolicyGated(event evidence.Event) bool {
	status := strings.ToLower(strings.TrimSpace(event.Status))
	summary := strings.ToLower(strings.TrimSpace(event.Summary))
	if event.Type == evidence.EventPreflight && status == "skipped" {
		return true
	}
	if strings.Contains(summary, "policy") && (strings.Contains(summary, "skip") || strings.Contains(summary, "gate")) {
		return true
	}
	for _, key := range []string{"outcome", "blocker", "failure_category", "category", "limit_reason", "reason", "policy_reason"} {
		value := strings.ToLower(firstPayloadString(event.Payload, key))
		if strings.Contains(value, "policy") && (strings.Contains(value, "skip") || strings.Contains(value, "gate") || strings.Contains(value, "denied")) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
