package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/worker"
)

// babysitOptions holds all flags for the babysit command.
type babysitOptions struct {
	pr            int
	repo          string
	interval      time.Duration
	timeout       time.Duration
	maxIterations int
	chatID        int64
	workDir       string
}

func newBabysitCommand(cfg *config.Config) *cobra.Command {
	opts := &babysitOptions{}

	cmd := &cobra.Command{
		Use:   "babysit",
		Short: "Auto-maintain a PR: fix CI, address reviews, rebase on conflicts",
		Long: `Babysit monitors a GitHub PR on a regular interval and automatically:
  - Fixes CI failures by invoking Claude to read logs and push a fix
  - Addresses review comments by invoking Claude to apply changes and push
  - Rebases on merge conflicts using git rebase
  - Sends Telegram notifications on significant events
  - Stops when the PR is merged or the timeout/iteration limit is reached`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.pr <= 0 {
				return fmt.Errorf("--pr must be a positive PR number")
			}
			return runBabysit(cmd.Context(), cfg, opts)
		},
	}

	cmd.Flags().IntVar(&opts.pr, "pr", 0, "GitHub PR number to babysit (required)")
	cmd.Flags().StringVar(&opts.repo, "repo", "", "GitHub repo in owner/repo format (default: inferred from git remote)")
	cmd.Flags().DurationVar(&opts.interval, "interval", 5*time.Minute, "check interval (e.g. 1m, 5m, 10m)")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 2*time.Hour, "stop after this duration (0 = no timeout)")
	cmd.Flags().IntVar(&opts.maxIterations, "max-iterations", 0, "stop after this many iterations (0 = unlimited)")
	cmd.Flags().Int64Var(&opts.chatID, "chat-id", 0, "Telegram chat ID to send notifications to")
	cmd.Flags().StringVar(&opts.workDir, "work-dir", "", "working directory for Claude (defaults to current directory)")

	_ = cmd.MarkFlagRequired("pr")

	return cmd
}

// runBabysit is the main babysit loop.
func runBabysit(ctx context.Context, cfg *config.Config, opts *babysitOptions) error {
	repo, err := resolveRepo(opts.repo)
	if err != nil {
		return fmt.Errorf("could not resolve repo: %w", err)
	}

	workDir := opts.workDir
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	notifier := newBabysitNotifier(cfg.Telegram.Token, opts.chatID)

	log.Printf("[babysit] starting PR #%d on %s, interval=%s", opts.pr, repo, opts.interval)
	notifier.send(fmt.Sprintf("🍼 *Babysit started* — PR #%d on `%s`\nChecking every %s", opts.pr, repo, opts.interval))

	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}

	claudeAdapter := worker.NewClaudeAdapter(worker.ClaudeConfig{WorkDir: workDir})

	ticker := time.NewTicker(opts.interval)
	defer ticker.Stop()

	iteration := 0

	for {
		iteration++
		if opts.maxIterations > 0 && iteration > opts.maxIterations {
			msg := fmt.Sprintf("⏹ Babysit stopped: reached max iterations (%d)", opts.maxIterations)
			log.Printf("[babysit] %s", msg)
			notifier.send(msg)
			return nil
		}

		if err := runBabysitIteration(ctx, repo, opts.pr, workDir, claudeAdapter, notifier); err != nil {
			// context cancelled = graceful shutdown
			if ctx.Err() != nil {
				msg := "⏹ Babysit stopped: context cancelled"
				log.Printf("[babysit] %s", msg)
				notifier.send(msg)
				return nil
			}
			log.Printf("[babysit] iteration %d error: %v", iteration, err)
		}

		select {
		case <-ctx.Done():
			msg := "⏹ Babysit stopped: timeout reached"
			if ctx.Err() == context.Canceled {
				msg = "⏹ Babysit stopped: interrupted"
			}
			log.Printf("[babysit] %s", msg)
			notifier.send(msg)
			return nil
		case <-ticker.C:
		}
	}
}

// runBabysitIteration performs one check-and-fix cycle.
// Returns a non-nil errStopBabysit sentinel when the loop should stop (PR merged).
func runBabysitIteration(ctx context.Context, repo string, pr int, workDir string, claude worker.Adapter, notifier *babysitNotifier) error {
	prNum := strconv.Itoa(pr)

	// --- Check if PR is merged/closed ---
	state, err := ghPRState(ctx, repo, prNum)
	if err != nil {
		log.Printf("[babysit] could not check PR state: %v", err)
		return nil // treat as transient; try again next iteration
	}
	switch state {
	case "MERGED":
		msg := fmt.Sprintf("✅ PR #%d merged — babysit complete", pr)
		log.Printf("[babysit] %s", msg)
		notifier.send(msg)
		return fmt.Errorf("PR merged") // caller checks ctx; returning error causes exit
	case "CLOSED":
		msg := fmt.Sprintf("🔴 PR #%d closed — babysit complete", pr)
		log.Printf("[babysit] %s", msg)
		notifier.send(msg)
		return fmt.Errorf("PR closed")
	}

	// --- Check for merge conflicts ---
	if err := checkAndRebasePR(ctx, repo, prNum, workDir, notifier); err != nil {
		log.Printf("[babysit] rebase check error: %v", err)
	}

	// --- Check CI status ---
	if err := checkAndFixCI(ctx, repo, prNum, workDir, pr, claude, notifier); err != nil {
		log.Printf("[babysit] CI check error: %v", err)
	}

	// --- Address review comments ---
	if err := checkAndAddressReviews(ctx, repo, prNum, workDir, pr, claude, notifier); err != nil {
		log.Printf("[babysit] review check error: %v", err)
	}

	return nil
}

// ghPRState returns the PR state: OPEN, MERGED, or CLOSED.
func ghPRState(ctx context.Context, repo, prNum string) (string, error) {
	out, err := ghJSON(ctx, repo, "pr", "view", prNum, "--json", "state")
	if err != nil {
		return "", err
	}
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("parse PR state: %w", err)
	}
	return v.State, nil
}

// checkAndRebasePR checks for merge conflicts and rebases if needed.
func checkAndRebasePR(ctx context.Context, repo, prNum, workDir string, notifier *babysitNotifier) error {
	out, err := ghJSON(ctx, repo, "pr", "view", prNum, "--json", "mergeable,headRefName,baseRefName")
	if err != nil {
		return fmt.Errorf("gh pr view: %w", err)
	}
	var v struct {
		Mergeable   string `json:"mergeable"`
		HeadRefName string `json:"headRefName"`
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return fmt.Errorf("parse mergeable: %w", err)
	}

	if v.Mergeable != "CONFLICTING" {
		return nil
	}

	log.Printf("[babysit] PR has merge conflicts, rebasing onto %s", v.BaseRefName)
	notifier.send(fmt.Sprintf("⚠️ Merge conflicts detected — rebasing onto `%s`", v.BaseRefName))

	// Fetch latest base and rebase.
	cmds := [][]string{
		{"git", "fetch", "origin"},
		{"git", "rebase", "origin/" + v.BaseRefName},
	}
	for _, args := range cmds {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			// Abort rebase to leave repo clean.
			_ = exec.CommandContext(ctx, "git", "rebase", "--abort").Run()
			msg := fmt.Sprintf("❌ Rebase failed:\n```\n%s\n```", string(out))
			notifier.send(msg)
			return fmt.Errorf("rebase failed: %w", err)
		}
	}

	// Force-push the rebased branch.
	pushCmd := exec.CommandContext(ctx, "git", "push", "--force-with-lease")
	pushCmd.Dir = workDir
	if out, err := pushCmd.CombinedOutput(); err != nil {
		msg := fmt.Sprintf("❌ Push after rebase failed:\n```\n%s\n```", string(out))
		notifier.send(msg)
		return fmt.Errorf("push failed: %w", err)
	}

	notifier.send("✅ Rebased and pushed successfully")
	return nil
}

// ghRunStatus describes a CI run status summary.
type ghRunStatus struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"url"`
}

// checkAndFixCI checks CI status and invokes Claude to fix failures.
func checkAndFixCI(ctx context.Context, repo, prNum, workDir string, pr int, claude worker.Adapter, notifier *babysitNotifier) error {
	out, err := runGH(ctx, "pr", "checks", strconv.Itoa(pr), "--repo", repo, "--json", "name,status,conclusion,link")
	if err != nil {
		// gh pr checks exits non-zero when checks are failing; parse what we got
		log.Printf("[babysit] gh pr checks: %v (output follows)", err)
	}
	if len(out) == 0 {
		return nil
	}

	var checks []struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Link       string `json:"link"`
	}
	if err := json.Unmarshal(out, &checks); err != nil {
		return fmt.Errorf("parse checks: %w", err)
	}

	var failed []string
	for _, c := range checks {
		if c.Conclusion == "failure" || c.Conclusion == "timed_out" {
			failed = append(failed, c.Name)
		}
	}

	if len(failed) == 0 {
		return nil
	}

	notifier.send(fmt.Sprintf("🔴 CI failures detected: %s\nInvoking Claude to fix…", strings.Join(failed, ", ")))
	log.Printf("[babysit] CI failures: %v — asking Claude to fix", failed)

	// Build a prompt for Claude to investigate and fix the failures.
	prompt := fmt.Sprintf(`You are babysitting PR #%d in repo %s.
The following CI checks are failing: %s

Steps to fix:
1. Run the failing checks/tests locally to reproduce the error.
2. Read the error output carefully.
3. Make the minimal code changes needed to fix the failures.
4. Run go fmt ./... and go vet ./... if this is a Go project.
5. Commit the fix with a short message.
6. Push the branch.

Working directory: %s
Do not ask for confirmation — proceed autonomously.`, pr, repo, strings.Join(failed, ", "), workDir)

	result, err := claude.Run(ctx, worker.Request{Task: prompt, WorkDir: workDir})
	if err != nil {
		msg := fmt.Sprintf("❌ Claude CI fix failed: %v", err)
		notifier.send(msg)
		return fmt.Errorf("claude run: %w", err)
	}

	summary := truncateStr(result.Content, 500)
	notifier.send(fmt.Sprintf("🤖 Claude CI fix applied:\n```\n%s\n```", summary))
	return nil
}

// checkAndAddressReviews checks for unresolved review comments and invokes Claude to address them.
func checkAndAddressReviews(ctx context.Context, repo, prNum, workDir string, pr int, claude worker.Adapter, notifier *babysitNotifier) error {
	out, err := ghJSON(ctx, repo, "pr", "view", prNum, "--json", "reviews,reviewRequests")
	if err != nil {
		return fmt.Errorf("gh pr view: %w", err)
	}

	var v struct {
		Reviews []struct {
			State string `json:"state"`
			Body  string `json:"body"`
		} `json:"reviews"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return fmt.Errorf("parse reviews: %w", err)
	}

	// Collect reviews requesting changes.
	var changesRequested []string
	for _, r := range v.Reviews {
		if r.State == "CHANGES_REQUESTED" && r.Body != "" {
			changesRequested = append(changesRequested, r.Body)
		}
	}
	if len(changesRequested) == 0 {
		return nil
	}

	// Also fetch inline review comments.
	commentsOut, err := runGH(ctx, "api", fmt.Sprintf("repos/%s/pulls/%d/comments", repo, pr))
	if err != nil {
		log.Printf("[babysit] could not fetch inline comments: %v", err)
	}

	var inlineComments []struct {
		Body     string `json:"body"`
		Path     string `json:"path"`
		Line     int    `json:"line"`
		Position int    `json:"position"`
	}
	if len(commentsOut) > 0 {
		_ = json.Unmarshal(commentsOut, &inlineComments)
	}

	notifier.send(fmt.Sprintf("💬 %d review(s) requesting changes — invoking Claude to address…", len(changesRequested)))
	log.Printf("[babysit] %d reviews requesting changes", len(changesRequested))

	var commentSummary strings.Builder
	for i, body := range changesRequested {
		commentSummary.WriteString(fmt.Sprintf("Review %d:\n%s\n\n", i+1, body))
	}
	for _, ic := range inlineComments {
		commentSummary.WriteString(fmt.Sprintf("Inline comment on %s line %d:\n%s\n\n", ic.Path, ic.Line, ic.Body))
	}

	prompt := fmt.Sprintf(`You are babysitting PR #%d in repo %s.
The following reviewer feedback needs to be addressed:

%s

Steps:
1. Read the review comments carefully.
2. Make the necessary code changes to address each comment.
3. Run go fmt ./... and go vet ./... if this is a Go project.
4. Commit with a message like "address review comments".
5. Push the branch.

Working directory: %s
Do not ask for confirmation — proceed autonomously.`, pr, repo, commentSummary.String(), workDir)

	result, err := claude.Run(ctx, worker.Request{Task: prompt, WorkDir: workDir})
	if err != nil {
		msg := fmt.Sprintf("❌ Claude review fix failed: %v", err)
		notifier.send(msg)
		return fmt.Errorf("claude run: %w", err)
	}

	summary := truncateStr(result.Content, 500)
	notifier.send(fmt.Sprintf("🤖 Claude addressed review comments:\n```\n%s\n```", summary))
	return nil
}

// --- Helpers ---

// ghJSON runs a gh command and returns the JSON output as bytes.
func ghJSON(ctx context.Context, repo string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"--repo", repo}, args...)
	return runGH(ctx, fullArgs...)
}

// runGH runs a gh CLI command and returns stdout.
func runGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Return whatever stdout we got (gh often writes JSON even on non-zero exit)
		return stdout.Bytes(), fmt.Errorf("gh %s: %w (stderr: %s)", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// resolveRepo returns the owner/repo string, inferring from git remote if not set.
func resolveRepo(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	out, err := exec.Command("gh", "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner").Output()
	if err != nil {
		return "", fmt.Errorf("gh repo view: %w", err)
	}
	repo := strings.TrimSpace(string(out))
	if repo == "" {
		return "", fmt.Errorf("could not determine repo from current directory")
	}
	return repo, nil
}

// truncateStr truncates s to at most maxLen runes.
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}

// babysitNotifier sends Telegram messages when a token and chat ID are configured.
type babysitNotifier struct {
	token  string
	chatID int64
}

func newBabysitNotifier(token string, chatID int64) *babysitNotifier {
	return &babysitNotifier{token: token, chatID: chatID}
}

func (n *babysitNotifier) send(text string) {
	if n.token == "" || n.chatID == 0 {
		return
	}
	payload := map[string]interface{}{
		"chat_id":    n.chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[babysit] telegram marshal error: %v", err)
		return
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.token)
	resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:noctx
	if err != nil {
		log.Printf("[babysit] telegram send error: %v", err)
		return
	}
	resp.Body.Close()
}
