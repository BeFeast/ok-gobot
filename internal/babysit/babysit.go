// Package babysit implements the PR babysit loop: automatically maintains a
// pull request by watching CI status, review comments, and merge conflicts,
// using Claude to fix issues and pushing updates.
package babysit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Config holds all parameters for a babysit run.
type Config struct {
	// Repo is the "owner/repo" identifier. If empty, inferred from git remote.
	Repo string
	// PR is the pull request number to watch.
	PR int
	// Interval between checks.
	Interval time.Duration
	// Timeout is the maximum total runtime. Zero means no timeout.
	Timeout time.Duration
	// MaxIterations caps the number of check loops. Zero means unlimited.
	MaxIterations int
	// WorkDir is the working directory for git and claude commands.
	WorkDir string
	// ClaudeBinary is the path to the claude CLI. Defaults to "claude".
	ClaudeBinary string
	// TelegramToken is the bot token for Telegram notifications (optional).
	TelegramToken string
	// NotifyChatID is the Telegram chat ID to send notifications to (optional).
	NotifyChatID int64
}

// Watcher runs the babysit loop.
type Watcher struct {
	cfg Config
	out io.Writer
}

// New creates a new Watcher.
func New(cfg Config, out io.Writer) *Watcher {
	if cfg.ClaudeBinary == "" {
		cfg.ClaudeBinary = "claude"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Minute
	}
	return &Watcher{cfg: cfg, out: out}
}

// Run starts the babysit loop and blocks until the PR is merged, the context
// is cancelled, the timeout is reached, or max iterations are exhausted.
func (w *Watcher) Run(ctx context.Context) error {
	if w.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.cfg.Timeout)
		defer cancel()
	}

	w.printf("Babysitting PR #%d (repo: %s, interval: %s)\n",
		w.cfg.PR, w.repoStr(), w.cfg.Interval)
	w.notify(ctx, fmt.Sprintf("Starting babysit for PR #%d (%s)", w.cfg.PR, w.repoStr()))

	for iteration := 1; ; iteration++ {
		if w.cfg.MaxIterations > 0 && iteration > w.cfg.MaxIterations {
			msg := fmt.Sprintf("Reached max iterations (%d), stopping", w.cfg.MaxIterations)
			w.printf("%s\n", msg)
			w.notify(ctx, msg)
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		w.printf("[iter %d] Checking PR #%d...\n", iteration, w.cfg.PR)

		merged, err := w.isPRMerged(ctx)
		if err != nil {
			w.printf("[iter %d] Failed to check PR state: %v\n", iteration, err)
		} else if merged {
			msg := fmt.Sprintf("PR #%d is merged, stopping babysit", w.cfg.PR)
			w.printf("%s\n", msg)
			w.notify(ctx, msg)
			return nil
		}

		// Check CI status.
		if acted, err := w.handleCI(ctx, iteration); err != nil {
			w.printf("[iter %d] CI check error: %v\n", iteration, err)
		} else if acted {
			// After acting, wait before next check so CI can pick up the push.
			w.waitOrCancel(ctx)
			continue
		}

		// Check review comments.
		if acted, err := w.handleReviews(ctx, iteration); err != nil {
			w.printf("[iter %d] Review check error: %v\n", iteration, err)
		} else if acted {
			w.waitOrCancel(ctx)
			continue
		}

		// Check merge conflicts.
		if acted, err := w.handleConflicts(ctx, iteration); err != nil {
			w.printf("[iter %d] Conflict check error: %v\n", iteration, err)
		} else if acted {
			w.waitOrCancel(ctx)
			continue
		}

		w.printf("[iter %d] All clear, waiting %s\n", iteration, w.cfg.Interval)
		w.waitOrCancel(ctx)
	}
}

// ---- PR state ----

func (w *Watcher) isPRMerged(ctx context.Context) (bool, error) {
	args := []string{"pr", "view", strconv.Itoa(w.cfg.PR), "--json", "state"}
	if w.cfg.Repo != "" {
		args = append(args, "--repo", w.cfg.Repo)
	}
	out, err := w.gh(ctx, args...)
	if err != nil {
		return false, err
	}
	var res struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return false, fmt.Errorf("parse pr state: %w", err)
	}
	return strings.EqualFold(res.State, "MERGED"), nil
}

// ---- CI handling ----

type checkRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
}

func (w *Watcher) ciCheckRuns(ctx context.Context) ([]checkRun, error) {
	args := []string{"pr", "checks", strconv.Itoa(w.cfg.PR), "--json", "name,status,conclusion"}
	if w.cfg.Repo != "" {
		args = append(args, "--repo", w.cfg.Repo)
	}
	out, err := w.gh(ctx, args...)
	if err != nil {
		return nil, err
	}
	var runs []checkRun
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		return nil, fmt.Errorf("parse checks: %w", err)
	}
	return runs, nil
}

func (w *Watcher) handleCI(ctx context.Context, iter int) (acted bool, err error) {
	runs, err := w.ciCheckRuns(ctx)
	if err != nil {
		return false, err
	}

	var failing []string
	for _, r := range runs {
		if strings.EqualFold(r.Status, "COMPLETED") &&
			(strings.EqualFold(r.Conclusion, "FAILURE") || strings.EqualFold(r.Conclusion, "TIMED_OUT")) {
			failing = append(failing, r.Name)
		}
	}

	if len(failing) == 0 {
		return false, nil
	}

	msg := fmt.Sprintf("PR #%d: CI failing: %s — asking Claude to fix", w.cfg.PR, strings.Join(failing, ", "))
	w.printf("[iter %d] %s\n", iter, msg)
	w.notify(ctx, msg)

	task := fmt.Sprintf(
		"The following CI checks are failing for PR #%d: %s.\n"+
			"Please investigate the failures, fix the code, commit the changes, and push to the PR branch.\n"+
			"Do not create a new PR. Work in the current branch.",
		w.cfg.PR, strings.Join(failing, ", "),
	)

	if err := w.runClaude(ctx, task); err != nil {
		w.printf("[iter %d] Claude CI fix failed: %v\n", iter, err)
		w.notify(ctx, fmt.Sprintf("PR #%d: Claude failed to fix CI: %v", w.cfg.PR, err))
		return false, nil
	}

	fixMsg := fmt.Sprintf("PR #%d: Claude pushed a CI fix for: %s", w.cfg.PR, strings.Join(failing, ", "))
	w.printf("[iter %d] %s\n", iter, fixMsg)
	w.notify(ctx, fixMsg)
	return true, nil
}

// ---- Review handling ----

type reviewComment struct {
	Body string `json:"body"`
	Path string `json:"path"`
}

func (w *Watcher) unresolvedReviewComments(ctx context.Context) ([]reviewComment, error) {
	args := []string{"pr", "view", strconv.Itoa(w.cfg.PR), "--json", "reviewRequests,reviews,comments"}
	if w.cfg.Repo != "" {
		args = append(args, "--repo", w.cfg.Repo)
	}

	// Use review-comment endpoint instead.
	commentArgs := []string{
		"api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/comments", w.cfg.PR),
		"--jq", `[.[] | select(.in_reply_to_id == null) | {body: .body, path: .path}]`,
	}
	if w.cfg.Repo != "" {
		commentArgs = append(commentArgs, "--repo", w.cfg.Repo)
	}
	_ = args // suppress unused warning; using commentArgs below

	out, err := w.gh(ctx, commentArgs...)
	if err != nil {
		return nil, err
	}
	var comments []reviewComment
	if err := json.Unmarshal([]byte(out), &comments); err != nil {
		return nil, fmt.Errorf("parse review comments: %w", err)
	}
	return comments, nil
}

func (w *Watcher) handleReviews(ctx context.Context, iter int) (acted bool, err error) {
	comments, err := w.unresolvedReviewComments(ctx)
	if err != nil {
		return false, err
	}
	if len(comments) == 0 {
		return false, nil
	}

	var parts []string
	for _, c := range comments {
		if c.Path != "" {
			parts = append(parts, fmt.Sprintf("File %s: %s", c.Path, c.Body))
		} else {
			parts = append(parts, c.Body)
		}
	}

	msg := fmt.Sprintf("PR #%d: %d review comment(s) to address — asking Claude", w.cfg.PR, len(comments))
	w.printf("[iter %d] %s\n", iter, msg)
	w.notify(ctx, msg)

	task := fmt.Sprintf(
		"PR #%d has the following review comments that need to be addressed:\n\n%s\n\n"+
			"Please address each comment, commit the changes, and push to the PR branch.\n"+
			"Do not create a new PR. Work in the current branch.",
		w.cfg.PR, strings.Join(parts, "\n---\n"),
	)

	if err := w.runClaude(ctx, task); err != nil {
		w.printf("[iter %d] Claude review fix failed: %v\n", iter, err)
		w.notify(ctx, fmt.Sprintf("PR #%d: Claude failed to address reviews: %v", w.cfg.PR, err))
		return false, nil
	}

	fixMsg := fmt.Sprintf("PR #%d: Claude addressed %d review comment(s)", w.cfg.PR, len(comments))
	w.printf("[iter %d] %s\n", iter, fixMsg)
	w.notify(ctx, fixMsg)
	return true, nil
}

// ---- Conflict handling ----

func (w *Watcher) prMergeableState(ctx context.Context) (string, error) {
	args := []string{"pr", "view", strconv.Itoa(w.cfg.PR), "--json", "mergeable,mergeStateStatus"}
	if w.cfg.Repo != "" {
		args = append(args, "--repo", w.cfg.Repo)
	}
	out, err := w.gh(ctx, args...)
	if err != nil {
		return "", err
	}
	var res struct {
		Mergeable        string `json:"mergeable"`
		MergeStateStatus string `json:"mergeStateStatus"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return "", fmt.Errorf("parse mergeable: %w", err)
	}
	return res.Mergeable, nil
}

func (w *Watcher) handleConflicts(ctx context.Context, iter int) (acted bool, err error) {
	state, err := w.prMergeableState(ctx)
	if err != nil {
		return false, err
	}

	if !strings.EqualFold(state, "CONFLICTING") {
		return false, nil
	}

	msg := fmt.Sprintf("PR #%d: merge conflicts detected — asking Claude to rebase and resolve", w.cfg.PR)
	w.printf("[iter %d] %s\n", iter, msg)
	w.notify(ctx, msg)

	task := fmt.Sprintf(
		"PR #%d has merge conflicts with the base branch.\n"+
			"Please rebase the branch on top of the latest base branch, resolve any conflicts,\n"+
			"commit the result, and force-push to the PR branch.\n"+
			"Do not create a new PR. Work in the current branch.",
		w.cfg.PR,
	)

	if err := w.runClaude(ctx, task); err != nil {
		w.printf("[iter %d] Claude conflict fix failed: %v\n", iter, err)
		w.notify(ctx, fmt.Sprintf("PR #%d: Claude failed to resolve conflicts: %v", w.cfg.PR, err))
		return false, nil
	}

	fixMsg := fmt.Sprintf("PR #%d: Claude resolved merge conflicts and pushed", w.cfg.PR)
	w.printf("[iter %d] %s\n", iter, fixMsg)
	w.notify(ctx, fixMsg)
	return true, nil
}

// ---- Claude execution ----

func (w *Watcher) runClaude(ctx context.Context, task string) error {
	args := []string{"-p", "--output-format", "text", task}
	cmd := exec.CommandContext(ctx, w.cfg.ClaudeBinary, args...)
	if w.cfg.WorkDir != "" {
		cmd.Dir = w.cfg.WorkDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude: %w\noutput: %s", err, string(out))
	}
	log.Printf("[babysit] claude output: %s", strings.TrimSpace(string(out)))
	return nil
}

// ---- gh CLI helper ----

func (w *Watcher) gh(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	if w.cfg.WorkDir != "" {
		cmd.Dir = w.cfg.WorkDir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh %s: %w\noutput: %s", strings.Join(args[:min(len(args), 3)], " "), err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

// ---- Telegram notifications ----

func (w *Watcher) notify(ctx context.Context, text string) {
	if w.cfg.TelegramToken == "" || w.cfg.NotifyChatID == 0 {
		return
	}
	go func() {
		apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", w.cfg.TelegramToken)
		vals := url.Values{
			"chat_id": {strconv.FormatInt(w.cfg.NotifyChatID, 10)},
			"text":    {text},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(vals.Encode()))
		if err != nil {
			log.Printf("[babysit] notify request error: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[babysit] notify send error: %v", err)
			return
		}
		defer resp.Body.Close() //nolint:errcheck
		if resp.StatusCode >= 400 {
			log.Printf("[babysit] notify non-OK status: %d", resp.StatusCode)
		}
	}()
}

// ---- helpers ----

func (w *Watcher) repoStr() string {
	if w.cfg.Repo != "" {
		return w.cfg.Repo
	}
	return "(current)"
}

func (w *Watcher) printf(format string, args ...any) {
	fmt.Fprintf(w.out, format, args...)
}

func (w *Watcher) waitOrCancel(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(w.cfg.Interval):
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
