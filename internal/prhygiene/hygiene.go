package prhygiene

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DefaultStaleAfter is the default age for surfacing an otherwise quiet open PR.
const DefaultStaleAfter = 14 * 24 * time.Hour

// Kind identifies the primary merge-readiness blocker for an open PR.
type Kind string

const (
	KindGreptile     Kind = "greptile"
	KindCI           Kind = "ci"
	KindReview       Kind = "review"
	KindNonMergeable Kind = "non_mergeable"
	KindStale        Kind = "stale"
)

// PullRequest is the read-only GitHub metadata needed for PR hygiene diagnosis.
type PullRequest struct {
	Number         int
	Title          string
	State          string
	URL            string
	Draft          bool
	MergeState     string
	ReviewDecision string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Checks         []Check
	Reviews        []Review
	Comments       []Comment
}

// Check is one CI or app check from a PR status rollup.
type Check struct {
	Name         string
	WorkflowName string
	Status       string
	Conclusion   string
}

// Review is one PR review summary.
type Review struct {
	Author string
	State  string
	Body   string
}

// Comment is one PR timeline or review comment summary.
type Comment struct {
	Author string
	Body   string
}

// Options controls blocker diagnosis.
type Options struct {
	Now        time.Time
	StaleAfter time.Duration
}

// Blocker is the concise operator-facing reason a PR needs attention.
type Blocker struct {
	Number    int       `json:"number"`
	Title     string    `json:"title,omitempty"`
	URL       string    `json:"url,omitempty"`
	State     string    `json:"state,omitempty"`
	Kind      Kind      `json:"kind"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Diagnose returns the primary blocker for one open PR, if any.
func Diagnose(pr PullRequest, opts Options) (Blocker, bool) {
	if pr.Number <= 0 || !isOpen(pr.State) {
		return Blocker{}, false
	}
	opts = normalizeOptions(opts)

	if name, ok := greptileBlocker(pr); ok {
		return makeBlocker(pr, KindGreptile, fmt.Sprintf("greptile findings require attention (%s)", name)), true
	}
	if name, ok := failingCICheck(pr); ok {
		return makeBlocker(pr, KindCI, fmt.Sprintf("ci checks failing (%s)", name)), true
	}
	if reviewBlocked(pr) {
		return makeBlocker(pr, KindReview, "unresolved review feedback"), true
	}
	if reason, ok := nonMergeableReason(pr); ok {
		return makeBlocker(pr, KindNonMergeable, reason), true
	}
	if stale(pr, opts) {
		updated := lastUpdated(pr)
		return makeBlocker(pr, KindStale, fmt.Sprintf("stale: last updated %s", updated.UTC().Format(time.RFC3339))), true
	}

	return Blocker{}, false
}

// DiagnoseAll returns deterministic blocker results for open PRs.
func DiagnoseAll(prs []PullRequest, opts Options) []Blocker {
	blockers := make([]Blocker, 0, len(prs))
	for _, pr := range prs {
		blocker, ok := Diagnose(pr, opts)
		if ok {
			blockers = append(blockers, blocker)
		}
	}
	sort.SliceStable(blockers, func(i, j int) bool {
		pi := kindPriority(blockers[i].Kind)
		pj := kindPriority(blockers[j].Kind)
		if pi != pj {
			return pi < pj
		}
		if !blockers[i].UpdatedAt.Equal(blockers[j].UpdatedAt) {
			if blockers[i].UpdatedAt.IsZero() {
				return false
			}
			if blockers[j].UpdatedAt.IsZero() {
				return true
			}
			return blockers[i].UpdatedAt.Before(blockers[j].UpdatedAt)
		}
		return blockers[i].Number < blockers[j].Number
	})
	return blockers
}

// Fingerprint is stable across repeated polls while the PR metadata is unchanged.
func (b Blocker) Fingerprint() string {
	updated := ""
	if !b.UpdatedAt.IsZero() {
		updated = b.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf("#%d:%s:%s:%s", b.Number, strings.ToUpper(strings.TrimSpace(b.State)), b.Kind, updated)
}

// FormatAge returns a compact relative age for status output.
func FormatAge(now, then time.Time) string {
	if then.IsZero() {
		return "unknown"
	}
	if now.IsZero() {
		now = time.Now()
	}
	d := now.Sub(then)
	if d < 0 {
		d = 0
	}
	if d >= 48*time.Hour {
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
	if d >= 2*time.Hour {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d >= 2*time.Minute {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return fmt.Sprintf("%ds", int(d/time.Second))
}

func normalizeOptions(opts Options) Options {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = DefaultStaleAfter
	}
	return opts
}

func makeBlocker(pr PullRequest, kind Kind, reason string) Blocker {
	return Blocker{
		Number:    pr.Number,
		Title:     strings.TrimSpace(pr.Title),
		URL:       strings.TrimSpace(pr.URL),
		State:     normalizedState(pr.State),
		Kind:      kind,
		Reason:    reason,
		CreatedAt: pr.CreatedAt,
		UpdatedAt: lastUpdated(pr),
	}
}

func isOpen(state string) bool {
	state = strings.TrimSpace(state)
	return state == "" || strings.EqualFold(state, "open")
}

func normalizedState(state string) string {
	state = strings.TrimSpace(state)
	if state == "" {
		return "OPEN"
	}
	return strings.ToUpper(state)
}

func greptileBlocker(pr PullRequest) (string, bool) {
	for _, check := range pr.Checks {
		if !mentionsGreptile(check.Name, check.WorkflowName) || !checkFailing(check) {
			continue
		}
		return checkLabel(check), true
	}
	for _, review := range pr.Reviews {
		if !mentionsGreptile(review.Author, review.Body) {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(review.State), "CHANGES_REQUESTED") || bodySuggestsFindings(review.Body) {
			return "Greptile review", true
		}
	}
	for _, comment := range pr.Comments {
		if mentionsGreptile(comment.Author, comment.Body) && bodySuggestsFindings(comment.Body) {
			return "Greptile comment", true
		}
	}
	return "", false
}

func failingCICheck(pr PullRequest) (string, bool) {
	for _, check := range pr.Checks {
		if mentionsGreptile(check.Name, check.WorkflowName) || !checkFailing(check) {
			continue
		}
		return checkLabel(check), true
	}
	return "", false
}

func checkFailing(check Check) bool {
	conclusion := strings.ToLower(strings.TrimSpace(check.Conclusion))
	status := strings.ToLower(strings.TrimSpace(check.Status))
	switch conclusion {
	case "failure", "failed", "error", "timed_out", "action_required", "startup_failure", "cancelled", "stale":
		return true
	case "":
		switch status {
		case "failure", "failed", "error":
			return true
		}
	}
	return false
}

func reviewBlocked(pr PullRequest) bool {
	if strings.EqualFold(strings.TrimSpace(pr.ReviewDecision), "CHANGES_REQUESTED") {
		return true
	}
	for _, review := range pr.Reviews {
		if strings.EqualFold(strings.TrimSpace(review.State), "CHANGES_REQUESTED") {
			return true
		}
	}
	return false
}

func nonMergeableReason(pr PullRequest) (string, bool) {
	if pr.Draft {
		return "not mergeable: draft PR", true
	}
	mergeState := strings.ToUpper(strings.TrimSpace(pr.MergeState))
	switch mergeState {
	case "", "UNKNOWN", "CLEAN", "HAS_HOOKS":
		return "", false
	default:
		return fmt.Sprintf("not mergeable: merge_state=%s", mergeState), true
	}
}

func stale(pr PullRequest, opts Options) bool {
	updated := lastUpdated(pr)
	return !updated.IsZero() && opts.Now.Sub(updated) >= opts.StaleAfter
}

func lastUpdated(pr PullRequest) time.Time {
	if !pr.UpdatedAt.IsZero() {
		return pr.UpdatedAt
	}
	return pr.CreatedAt
}

func mentionsGreptile(values ...string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), "greptile") {
			return true
		}
	}
	return false
}

func bodySuggestsFindings(body string) bool {
	body = strings.ToLower(body)
	if strings.Contains(body, "no finding") || strings.Contains(body, "no issue") {
		return false
	}
	for _, marker := range []string{"requires attention", "require attention", "finding", "issue found", "issues found", "unresolved"} {
		if strings.Contains(body, marker) {
			return true
		}
	}
	return false
}

func checkLabel(check Check) string {
	if name := strings.TrimSpace(check.Name); name != "" {
		return name
	}
	if workflow := strings.TrimSpace(check.WorkflowName); workflow != "" {
		return workflow
	}
	return "unknown check"
}

func kindPriority(kind Kind) int {
	switch kind {
	case KindGreptile:
		return 0
	case KindCI:
		return 1
	case KindReview:
		return 2
	case KindNonMergeable:
		return 3
	case KindStale:
		return 4
	default:
		return 5
	}
}
