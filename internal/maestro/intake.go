package maestro

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	DefaultReadyLabel = "ready"
	DefaultLimit      = 50
)

var (
	dependencyLineRE = regexp.MustCompile(`(?i)^(?:depends(?:\s+on)?|dependencies|dependency|blocked\s+by|requires)(?:\s*:\s*|\s+)(.+)$`)
	issueRefRE       = regexp.MustCompile(`(?i)(?:([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+))?#(\d+)`)
)

// Issue is the GitHub issue data needed by the intake policy.
type Issue struct {
	Number int
	Title  string
	Body   string
	Labels []string
	State  string
}

// DependencyRef identifies one dependency issue or PR reference.
type DependencyRef struct {
	Repo   string
	Number int
	Raw    string
}

func (r DependencyRef) String() string {
	if r.Raw != "" {
		return r.Raw
	}
	if r.Repo != "" {
		return fmt.Sprintf("%s#%d", r.Repo, r.Number)
	}
	return fmt.Sprintf("#%d", r.Number)
}

// DependencyStatus is the resolved state of a dependency reference.
type DependencyStatus struct {
	Ref           DependencyRef
	State         string
	IsPullRequest bool
	MergeReady    bool
	MergeState    string
	Draft         bool
}

// Satisfied reports whether the dependency gate allows the candidate issue.
func (s DependencyStatus) Satisfied() bool {
	state := strings.ToLower(strings.TrimSpace(s.State))
	if state == "closed" || state == "merged" {
		return true
	}
	return s.IsPullRequest && !s.Draft && s.MergeReady
}

func (s DependencyStatus) unsatisfiedReason(ref DependencyRef) string {
	label := ref.String()
	state := strings.ToLower(strings.TrimSpace(s.State))
	if state == "" {
		state = "unknown"
	}
	if s.IsPullRequest {
		mergeState := strings.TrimSpace(s.MergeState)
		if mergeState == "" {
			mergeState = "unknown"
		}
		if s.Draft {
			return fmt.Sprintf("dependency %s is a draft PR", label)
		}
		return fmt.Sprintf("dependency %s is not merge-ready (state=%s, merge_state=%s)", label, state, mergeState)
	}
	return fmt.Sprintf("dependency %s is %s", label, state)
}

// Source provides issue candidates and dependency state.
type Source interface {
	ListOpenIssues(ctx context.Context, repo string, limit int) ([]Issue, error)
	ResolveDependency(ctx context.Context, repo string, ref DependencyRef) (DependencyStatus, error)
}

// Policy controls issue intake gates.
type Policy struct {
	Repo              string
	ReadyLabel        string
	HardExcludeLabels []string
	Limit             int
	Override          bool
	OverrideReason    string
}

// CandidateDecision records the policy outcome for one issue.
type CandidateDecision struct {
	Issue           Issue
	Eligible        bool
	SkipReasons     []string
	Dependencies    []DependencyRef
	OverrideUsed    bool
	OverrideReasons []string
}

// Decision is the complete dry-run/status result.
type Decision struct {
	Policy  Policy
	Next    *CandidateDecision
	Skipped []CandidateDecision
}

// DefaultHardExcludeLabels returns the labels that are never eligible unless a
// maintainer explicitly uses the override flag.
func DefaultHardExcludeLabels() []string {
	return []string{"blocked", "epic", "meta", "question", "wontfix", "duplicate", "invalid"}
}

// DryRun lists open issues from source and evaluates the strict intake policy.
func DryRun(ctx context.Context, source Source, policy Policy) (Decision, error) {
	policy = NormalizePolicy(policy)
	issues, err := source.ListOpenIssues(ctx, policy.Repo, policy.Limit)
	if err != nil {
		return Decision{}, err
	}
	return Evaluate(ctx, issues, source, policy), nil
}

// Evaluate applies the intake policy to already-loaded candidate issues.
func Evaluate(ctx context.Context, issues []Issue, resolver Source, policy Policy) Decision {
	policy = NormalizePolicy(policy)
	decision := Decision{Policy: policy}

	for _, issue := range issues {
		candidate := evaluateIssue(ctx, issue, resolver, policy)
		if policy.Override {
			candidate.Eligible = true
			candidate.OverrideUsed = true
			candidate.OverrideReasons = append([]string(nil), candidate.SkipReasons...)
			candidate.SkipReasons = nil
			decision.Next = &candidate
			return decision
		}
		if candidate.Eligible {
			decision.Next = &candidate
			return decision
		}
		decision.Skipped = append(decision.Skipped, candidate)
	}

	return decision
}

// NormalizePolicy fills strict safe defaults.
func NormalizePolicy(policy Policy) Policy {
	policy.ReadyLabel = strings.TrimSpace(policy.ReadyLabel)
	if policy.ReadyLabel == "" {
		policy.ReadyLabel = DefaultReadyLabel
	}
	if policy.Limit <= 0 {
		policy.Limit = DefaultLimit
	}
	policy.HardExcludeLabels = normalizeLabels(policy.HardExcludeLabels)
	if len(policy.HardExcludeLabels) == 0 {
		policy.HardExcludeLabels = DefaultHardExcludeLabels()
	}
	return policy
}

func evaluateIssue(ctx context.Context, issue Issue, resolver Source, policy Policy) CandidateDecision {
	labels := labelSet(issue.Labels)
	candidate := CandidateDecision{Issue: issue}

	if _, ok := labels[strings.ToLower(policy.ReadyLabel)]; !ok {
		candidate.SkipReasons = append(candidate.SkipReasons, fmt.Sprintf("missing ready label %q", policy.ReadyLabel))
	}

	for _, label := range policy.HardExcludeLabels {
		if _, ok := labels[strings.ToLower(label)]; ok {
			candidate.SkipReasons = append(candidate.SkipReasons, fmt.Sprintf("hard-exclude label %q", label))
		}
	}

	deps := ParseDependencyRefs(issue.Body)
	candidate.Dependencies = deps
	for _, dep := range deps {
		if resolver == nil {
			candidate.SkipReasons = append(candidate.SkipReasons, fmt.Sprintf("dependency %s status unavailable", dep.String()))
			continue
		}
		status, err := resolver.ResolveDependency(ctx, policy.Repo, dep)
		if err != nil {
			candidate.SkipReasons = append(candidate.SkipReasons, fmt.Sprintf("dependency %s status unavailable: %v", dep.String(), err))
			continue
		}
		if !status.Satisfied() {
			candidate.SkipReasons = append(candidate.SkipReasons, status.unsatisfiedReason(dep))
		}
	}

	candidate.Eligible = len(candidate.SkipReasons) == 0
	return candidate
}

// ParseDependencyRefs extracts dedicated dependency-line issue references while
// ignoring fenced code blocks, inline-code examples, and example sections.
func ParseDependencyRefs(body string) []DependencyRef {
	var refs []DependencyRef
	seen := map[string]struct{}{}
	inFence := false
	inExample := false

	for _, rawLine := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if isFenceLine(trimmed) {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if trimmed == "" {
			inExample = false
			continue
		}
		if isExampleHeading(trimmed) {
			inExample = true
			continue
		}
		if inExample || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "<!--") {
			continue
		}

		line := stripMarkdownListPrefix(trimmed)
		line = stripInlineCode(line)
		match := dependencyLineRE.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		for _, ref := range refsFromText(match[1]) {
			key := strings.ToLower(ref.String())
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			refs = append(refs, ref)
		}
	}

	return refs
}

func refsFromText(text string) []DependencyRef {
	matches := issueRefRE.FindAllStringSubmatch(text, -1)
	refs := make([]DependencyRef, 0, len(matches))
	for _, match := range matches {
		n, err := strconv.Atoi(match[2])
		if err != nil || n <= 0 {
			continue
		}
		raw := fmt.Sprintf("#%d", n)
		if match[1] != "" {
			raw = fmt.Sprintf("%s#%d", match[1], n)
		}
		refs = append(refs, DependencyRef{Repo: match[1], Number: n, Raw: raw})
	}
	return refs
}

func isFenceLine(line string) bool {
	return strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")
}

func isExampleHeading(line string) bool {
	line = strings.TrimLeft(line, "# ")
	lower := strings.ToLower(strings.TrimSpace(line))
	lower = strings.TrimSuffix(lower, ":")
	return lower == "example" || lower == "examples" || strings.HasPrefix(lower, "for example") || strings.HasPrefix(lower, "e.g.")
}

func stripMarkdownListPrefix(line string) string {
	line = strings.TrimSpace(line)
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			break
		}
	}
	if strings.HasPrefix(line, "[ ] ") || strings.HasPrefix(strings.ToLower(line), "[x] ") {
		line = strings.TrimSpace(line[4:])
	}
	if idx := strings.IndexAny(line, ".)"); idx > 0 {
		if _, err := strconv.Atoi(line[:idx]); err == nil {
			line = strings.TrimSpace(line[idx+1:])
		}
	}
	return line
}

func stripInlineCode(line string) string {
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		if line[i] != '`' {
			b.WriteByte(line[i])
			continue
		}
		i++
		for i < len(line) && line[i] != '`' {
			i++
		}
	}
	return b.String()
}

func labelSet(labels []string) map[string]struct{} {
	set := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		label = strings.ToLower(strings.TrimSpace(label))
		if label != "" {
			set[label] = struct{}{}
		}
	}
	return set
}

func normalizeLabels(labels []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.ToLower(strings.TrimSpace(label))
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		out = append(out, label)
	}
	sort.Strings(out)
	return out
}
