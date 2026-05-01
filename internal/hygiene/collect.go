package hygiene

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ok-gobot/internal/config"
	"ok-gobot/internal/maestro"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/worker"
)

type CommandRunner func(ctx context.Context, dir string, name string, args ...string) ([]byte, error)

type CollectOptions struct {
	Config            *config.Config
	Store             *storage.Store
	Repo              string
	RepoRoot          string
	SourceBranch      string
	UpstreamBranch    string
	Limit             int
	ReadyLabel        string
	HardExcludeLabels []string
	WorktreeStatePath string
	Approvals         []Approval
	Workers           []Worker
	Runner            CommandRunner
}

var (
	prRefRE    = regexp.MustCompile(`(?i)\b(?:pr|pull\s+request)\s*#?(\d+)`)
	issueRefRE = regexp.MustCompile(`(?i)\bissue\s*#?(\d+)|#(\d+)`)
)

func Collect(ctx context.Context, opts CollectOptions) (Snapshot, error) {
	now := time.Now()
	opts = normalizeCollectOptions(opts)
	snapshot := Snapshot{
		GeneratedAt: now,
		Approvals:   append([]Approval(nil), opts.Approvals...),
		Workers:     append([]Worker(nil), opts.Workers...),
	}

	if opts.Store != nil {
		collectStoredState(opts.Store, &snapshot)
	}

	collectWorktreeState(opts.WorktreeStatePath, &snapshot)

	repoRoot := strings.TrimSpace(opts.RepoRoot)
	if repoRoot == "" {
		if root, err := gitRepoRoot(ctx, opts.Runner); err == nil {
			repoRoot = root
		} else {
			snapshot.Warnings = append(snapshot.Warnings, "local git repository root unavailable: "+err.Error())
		}
	}
	if repoRoot != "" {
		checkout, err := collectCheckout(ctx, opts.Runner, repoRoot, opts.SourceBranch, opts.UpstreamBranch)
		if err != nil {
			snapshot.Warnings = append(snapshot.Warnings, "local checkout drift unavailable: "+err.Error())
		} else {
			snapshot.Checkout = checkout
		}
	}

	prs, err := collectPullRequests(ctx, opts.Runner, repoRoot, opts.Repo, opts.Limit)
	prsLoaded := err == nil
	if err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "GitHub PR state unavailable: "+err.Error())
	} else {
		snapshot.PullRequests = prs
	}

	queue, err := collectQueue(ctx, opts, repoRoot, prs, prsLoaded)
	if err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "Maestro queue state unavailable: "+err.Error())
	} else {
		snapshot.Queue = queue
	}

	attachPRContext(&snapshot)
	return snapshot, nil
}

func BuildReport(ctx context.Context, collect CollectOptions, analyze Options) (Report, error) {
	snapshot, err := Collect(ctx, collect)
	if err != nil {
		return Report{}, err
	}
	if analyze.Now.IsZero() {
		analyze.Now = snapshot.GeneratedAt
	}
	return Analyze(snapshot, analyze), nil
}

func normalizeCollectOptions(opts CollectOptions) CollectOptions {
	if opts.Config != nil {
		if opts.Repo == "" {
			opts.Repo = opts.Config.Maestro.Repo
		}
		if opts.ReadyLabel == "" {
			opts.ReadyLabel = opts.Config.Maestro.ReadyLabel
		}
		if len(opts.HardExcludeLabels) == 0 {
			opts.HardExcludeLabels = append([]string(nil), opts.Config.Maestro.HardExcludeLabels...)
		}
		if opts.Limit <= 0 {
			opts.Limit = opts.Config.Maestro.Limit
		}
	}
	if opts.Limit <= 0 {
		opts.Limit = maestro.DefaultLimit
	}
	if opts.ReadyLabel == "" {
		opts.ReadyLabel = maestro.DefaultReadyLabel
	}
	if len(opts.HardExcludeLabels) == 0 {
		opts.HardExcludeLabels = maestro.DefaultHardExcludeLabels()
	}
	if opts.SourceBranch == "" {
		opts.SourceBranch = "main"
	}
	if opts.UpstreamBranch == "" {
		opts.UpstreamBranch = "origin/" + opts.SourceBranch
	}
	if opts.WorktreeStatePath == "" {
		opts.WorktreeStatePath = DefaultWorktreeStatePath()
	}
	if opts.Runner == nil {
		opts.Runner = defaultCommandRunner
	}
	return opts
}

func collectStoredState(store *storage.Store, snapshot *Snapshot) {
	sessions, err := store.ListSessionsV2(500)
	if err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "session state unavailable: "+err.Error())
	} else {
		for _, session := range sessions {
			issue, pr := extractRefs(session.SessionKey + " " + session.LastSummary)
			snapshot.Sessions = append(snapshot.Sessions, Session{
				Key:         session.SessionKey,
				IssueNumber: issue,
				PRNumber:    pr,
				UpdatedAt:   parseTime(session.UpdatedAt),
			})
		}
	}

	jobs, err := store.ListJobs(500)
	if err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "job state unavailable: "+err.Error())
		return
	}
	for _, job := range jobs {
		issue, pr := extractRefs(job.Description + " " + job.Summary + " " + job.Error)
		snapshot.Jobs = append(snapshot.Jobs, Job{
			JobID:       job.JobID,
			SessionKey:  job.SessionKey,
			Status:      job.Status,
			IssueNumber: issue,
			PRNumber:    pr,
			Attempt:     job.Attempt,
			MaxAttempts: job.MaxAttempts,
			CreatedAt:   parseTime(job.CreatedAt),
			StartedAt:   parseTime(job.StartedAt),
			UpdatedAt:   parseTime(job.UpdatedAt),
		})
	}
}

func collectWorktreeState(statePath string, snapshot *Snapshot) {
	if strings.TrimSpace(statePath) == "" {
		return
	}
	entries, err := worker.NewWorktreeManager(statePath).List()
	if err != nil {
		snapshot.Warnings = append(snapshot.Warnings, "worktree state unavailable: "+err.Error())
		return
	}
	for _, entry := range entries {
		snapshot.Worktrees = append(snapshot.Worktrees, Worktree{
			ID:        entry.ID,
			Branch:    entry.Branch,
			Path:      entry.Path,
			RepoRoot:  entry.RepoRoot,
			Status:    string(entry.Status),
			PRNumber:  entry.PRNumber,
			PRState:   entry.PRStatus,
			CreatedAt: entry.CreatedAt,
		})
	}
}

func collectPullRequests(ctx context.Context, runner CommandRunner, repoRoot, repo string, limit int) ([]PullRequest, error) {
	args := []string{"pr", "list", "--state", "all", "--limit", strconv.Itoa(limit), "--json", "number,title,body,state,headRefName,labels,updatedAt,closingIssuesReferences"}
	if strings.TrimSpace(repo) != "" {
		args = append(args, "--repo", strings.TrimSpace(repo))
	}
	out, err := runner(ctx, repoRoot, "gh", args...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		Body        string `json:"body"`
		State       string `json:"state"`
		HeadRefName string `json:"headRefName"`
		Labels      []struct {
			Name string `json:"name"`
		} `json:"labels"`
		UpdatedAt               string `json:"updatedAt"`
		ClosingIssuesReferences []struct {
			Number int `json:"number"`
		} `json:"closingIssuesReferences"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh pr list: %w", err)
	}
	prs := make([]PullRequest, 0, len(raw))
	for _, item := range raw {
		issue := 0
		if len(item.ClosingIssuesReferences) > 0 {
			issue = item.ClosingIssuesReferences[0].Number
		}
		labels := make([]string, 0, len(item.Labels))
		for _, label := range item.Labels {
			labels = append(labels, label.Name)
		}
		prs = append(prs, PullRequest{
			Number:      item.Number,
			IssueNumber: issue,
			Title:       item.Title,
			Body:        item.Body,
			State:       strings.ToLower(strings.TrimSpace(item.State)),
			Branch:      item.HeadRefName,
			Labels:      labels,
			UpdatedAt:   parseTime(item.UpdatedAt),
		})
	}
	return prs, nil
}

func collectQueue(ctx context.Context, opts CollectOptions, repoRoot string, prs []PullRequest, prsLoaded bool) (Queue, error) {
	resolver := maestro.NewGHClient(repoRoot)
	policy := maestro.Policy{
		Repo:              opts.Repo,
		ReadyLabel:        opts.ReadyLabel,
		HardExcludeLabels: opts.HardExcludeLabels,
		Limit:             opts.Limit,
	}
	var decision maestro.Decision
	if prsLoaded {
		decision = maestro.Evaluate(ctx, issuesFromPullRequests(prs), resolver, policy)
	} else {
		var err error
		decision, err = maestro.DryRun(ctx, resolver, policy)
		if err != nil {
			return Queue{}, err
		}
	}
	queue := Queue{SkippedIssues: len(decision.Skipped), CheckedAt: time.Now()}
	if decision.Next != nil {
		queue.EligibleIssues = 1
	}
	reasons := map[string]struct{}{}
	for _, skipped := range decision.Skipped {
		for _, reason := range skipped.SkipReasons {
			reason = strings.TrimSpace(reason)
			if reason == "" {
				continue
			}
			reasons[reason] = struct{}{}
		}
	}
	for reason := range reasons {
		queue.Reasons = append(queue.Reasons, reason)
	}
	sort.Strings(queue.Reasons)
	if len(queue.Reasons) > 3 {
		queue.Reasons = queue.Reasons[:3]
	}
	return queue, nil
}

func issuesFromPullRequests(prs []PullRequest) []maestro.Issue {
	issues := make([]maestro.Issue, 0, len(prs))
	for _, pr := range prs {
		if !isOpenState(pr.State) {
			continue
		}
		issues = append(issues, maestro.Issue{
			Number: pr.Number,
			Title:  pr.Title,
			Body:   pr.Body,
			Labels: append([]string(nil), pr.Labels...),
			State:  pr.State,
		})
	}
	return issues
}

func collectCheckout(ctx context.Context, runner CommandRunner, repoRoot, branch, upstream string) (Checkout, error) {
	refPair := upstream + "..." + branch
	out, err := runner(ctx, repoRoot, "git", "rev-list", "--left-right", "--count", refPair)
	if err != nil {
		return Checkout{}, err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 {
		return Checkout{}, fmt.Errorf("unexpected git rev-list output %q", strings.TrimSpace(string(out)))
	}
	behind, err := strconv.Atoi(fields[0])
	if err != nil {
		return Checkout{}, fmt.Errorf("parse behind count: %w", err)
	}
	ahead, err := strconv.Atoi(fields[1])
	if err != nil {
		return Checkout{}, fmt.Errorf("parse ahead count: %w", err)
	}
	return Checkout{Branch: branch, Upstream: upstream, BehindBy: behind, AheadBy: ahead, CheckedAt: time.Now()}, nil
}

func attachPRContext(snapshot *Snapshot) {
	prByBranch := map[string]PullRequest{}
	prByNumber := map[int]PullRequest{}
	for _, pr := range snapshot.PullRequests {
		if pr.Branch != "" {
			prByBranch[pr.Branch] = pr
		}
		if pr.Number > 0 {
			prByNumber[pr.Number] = pr
		}
	}
	for i, worktree := range snapshot.Worktrees {
		if worktree.PRNumber > 0 {
			if pr, ok := prByNumber[worktree.PRNumber]; ok {
				snapshot.Worktrees[i].PRState = valueOr(worktree.PRState, pr.State)
			}
			continue
		}
		if pr, ok := prByBranch[worktree.Branch]; ok {
			snapshot.Worktrees[i].PRNumber = pr.Number
			snapshot.Worktrees[i].PRState = valueOr(worktree.PRState, pr.State)
		}
	}
	for i, session := range snapshot.Sessions {
		if session.PRNumber > 0 {
			if pr, ok := prByNumber[session.PRNumber]; ok {
				snapshot.Sessions[i].IssueNumber = firstPositive(session.IssueNumber, pr.IssueNumber)
				snapshot.Sessions[i].Branch = valueOr(session.Branch, pr.Branch)
			}
		}
	}
}

func gitRepoRoot(ctx context.Context, runner CommandRunner) (string, error) {
	out, err := runner(ctx, "", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(out))
	if root == "" {
		return "", fmt.Errorf("git returned empty repository root")
	}
	return root, nil
}

func defaultCommandRunner(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return nil, err
		}
		return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return out, nil
}

func DefaultWorktreeStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".ok-gobot", "worktrees.json")
}

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05",
	}
	for _, format := range formats {
		if parsed, err := time.Parse(format, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func extractRefs(text string) (issueNumber int, prNumber int) {
	prMatches := prRefRE.FindAllStringSubmatchIndex(text, -1)
	if len(prMatches) > 0 && len(prMatches[0]) >= 4 && prMatches[0][2] >= 0 {
		prNumber, _ = strconv.Atoi(text[prMatches[0][2]:prMatches[0][3]])
	}
	for _, match := range issueRefRE.FindAllStringSubmatchIndex(text, -1) {
		if rangesOverlap(match[0], match[1], prMatches) {
			continue
		}
		if len(match) >= 4 && match[2] >= 0 {
			issueNumber, _ = strconv.Atoi(text[match[2]:match[3]])
			return issueNumber, prNumber
		}
		if len(match) >= 6 && match[4] >= 0 {
			issueNumber, _ = strconv.Atoi(text[match[4]:match[5]])
			return issueNumber, prNumber
		}
	}
	return issueNumber, prNumber
}

func rangesOverlap(start, end int, ranges [][]int) bool {
	for _, r := range ranges {
		if len(r) < 2 {
			continue
		}
		if start < r[1] && end > r[0] {
			return true
		}
	}
	return false
}
