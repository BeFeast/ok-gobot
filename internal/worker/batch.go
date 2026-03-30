package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ok-gobot/internal/logger"
)

// BatchConfig holds configuration for batch fan-out execution.
type BatchConfig struct {
	// MaxParallel is the maximum number of concurrent workers (default 5).
	MaxParallel int
	// WorkDir is the path to the git repository root.
	WorkDir string
	// Adapter is the AI worker adapter used for decomposition and subtask execution.
	Adapter Adapter
	// Model is the AI model to use (empty = adapter default).
	Model string
	// BaseBranch is the base git branch to branch from (default "main").
	BaseBranch string
	// Repo is the GitHub repo in "owner/name" format for PR creation (empty = skip PR).
	Repo string
	// SkipPR disables automatic PR creation.
	SkipPR bool
}

// Subtask is one unit of decomposed work.
type Subtask struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// SubtaskResult is the outcome of one parallel worker.
type SubtaskResult struct {
	Subtask  Subtask
	Branch   string
	Worktree string
	Output   string
	Error    error
}

// BatchResult is the final aggregated outcome of a batch run.
type BatchResult struct {
	BatchID     string
	Subtasks    []SubtaskResult
	MergeBranch string
	PRURL       string
	MergeErrors []string
}

// BatchRunner decomposes a task and executes subtasks in parallel git worktrees.
type BatchRunner struct {
	cfg BatchConfig
}

// NewBatchRunner creates a BatchRunner with the given config.
// Default: MaxParallel=5, BaseBranch="main".
func NewBatchRunner(cfg BatchConfig) *BatchRunner {
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 5
	}
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = "main"
	}
	return &BatchRunner{cfg: cfg}
}

// decompositionEnvelope is the expected AI response shape: {"subtasks": [...]}.
type decompositionEnvelope struct {
	Subtasks []Subtask `json:"subtasks"`
}

// Decompose uses the AI adapter to split a task into concrete subtasks.
// It asks the adapter for a JSON response and handles several common response
// shapes: plain array, {"subtasks":[...]} envelope, and JSON embedded in prose.
func (r *BatchRunner) Decompose(ctx context.Context, task string) ([]Subtask, error) {
	prompt := fmt.Sprintf(`You are a task decomposition assistant. Break the following task into concrete, independent subtasks for parallel execution in separate git worktrees.

Task: %s

Rules:
1. Return ONLY valid JSON — no prose before or after.
2. Use this exact schema: {"subtasks": [{"name": "snake_case_id", "description": "what to do"}]}
3. Each subtask must be:
   - Self-contained and independently executable
   - Specific about which files or directories it operates on
   - Achievable by an AI coding assistant in a single pass
4. Generate 2–8 subtasks. Prefer fewer, well-scoped tasks over many fine-grained ones.
5. "name" must be snake_case and at most 40 characters.`, task)

	result, err := r.cfg.Adapter.Run(ctx, Request{
		Task:    prompt,
		Model:   r.cfg.Model,
		WorkDir: r.cfg.WorkDir,
	})
	if err != nil {
		return nil, fmt.Errorf("decompose request: %w", err)
	}

	return parseSubtasks(result.Content)
}

// parseSubtasks extracts a []Subtask from an AI response string.
// Accepts: {"subtasks":[...]}, [...], or JSON embedded anywhere in the text.
func parseSubtasks(content string) ([]Subtask, error) {
	content = strings.TrimSpace(content)

	// 1. Try {"subtasks": [...]} envelope.
	var envelope decompositionEnvelope
	if err := json.Unmarshal([]byte(content), &envelope); err == nil && len(envelope.Subtasks) > 0 {
		return envelope.Subtasks, nil
	}

	// 2. Try plain array.
	var flat []Subtask
	if err := json.Unmarshal([]byte(content), &flat); err == nil && len(flat) > 0 {
		return flat, nil
	}

	// 3. Try to find a JSON object starting with '{' and parse it.
	if idx := strings.Index(content, "{"); idx >= 0 {
		var env decompositionEnvelope
		if err := json.Unmarshal([]byte(content[idx:]), &env); err == nil && len(env.Subtasks) > 0 {
			return env.Subtasks, nil
		}
	}

	// 4. Try to find a JSON array starting with '['.
	if start := strings.Index(content, "["); start >= 0 {
		if end := strings.LastIndex(content, "]"); end > start {
			var arr []Subtask
			if err := json.Unmarshal([]byte(content[start:end+1]), &arr); err == nil && len(arr) > 0 {
				return arr, nil
			}
		}
	}

	return nil, fmt.Errorf("could not parse subtasks from AI response: %s", content)
}

// Run decomposes the task, executes all subtasks in parallel worktrees,
// consolidates results into a merge branch, and optionally creates a PR.
func (r *BatchRunner) Run(ctx context.Context, task string, progressFn func(msg string)) (*BatchResult, error) {
	batchID := fmt.Sprintf("batch-%d", time.Now().Unix())

	// 1. Decompose.
	progressFn("Decomposing task into subtasks...")
	subtasks, err := r.Decompose(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("decompose: %w", err)
	}
	progressFn(fmt.Sprintf("Task decomposed into %d subtasks", len(subtasks)))

	// 2. Fetch base branch to ensure we're up to date.
	if r.cfg.WorkDir != "" {
		if out, ferr := r.git("fetch", "origin", r.cfg.BaseBranch); ferr != nil {
			logger.Debugf("[batch] git fetch origin %s: %v: %s", r.cfg.BaseBranch, ferr, out)
		}
	}

	// 3. Create worktree base directory.
	worktreeBase := filepath.Join(os.TempDir(), "ok-gobot-batch", batchID)
	if err := os.MkdirAll(worktreeBase, 0o755); err != nil {
		return nil, fmt.Errorf("create worktree base dir: %w", err)
	}
	defer func() {
		// Best-effort cleanup of the temp parent directory after all worktrees are removed.
		_ = os.RemoveAll(worktreeBase)
	}()

	// 4. Run subtasks in parallel, bounded by MaxParallel.
	sem := make(chan struct{}, r.cfg.MaxParallel)
	results := make([]SubtaskResult, len(subtasks))
	var wg sync.WaitGroup

	for i, st := range subtasks {
		wg.Add(1)
		go func(idx int, subtask Subtask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			progressFn(fmt.Sprintf("[%s] Starting worker...", subtask.Name))
			res := r.runSubtask(ctx, batchID, worktreeBase, subtask)
			results[idx] = res
			if res.Error != nil {
				progressFn(fmt.Sprintf("[%s] Failed: %v", subtask.Name, res.Error))
			} else {
				progressFn(fmt.Sprintf("[%s] Done", subtask.Name))
			}
		}(i, st)
	}
	wg.Wait()

	batchResult := &BatchResult{
		BatchID:  batchID,
		Subtasks: results,
	}

	if r.cfg.SkipPR {
		r.cleanupWorktrees(results)
		return batchResult, nil
	}

	// 5. Consolidate: create a merge branch and merge each successful subtask branch.
	progressFn("Consolidating subtask branches...")
	mergeBranch := fmt.Sprintf("batch/%s/consolidated", batchID)
	mergeWorktree := filepath.Join(worktreeBase, "consolidated")

	startPoint := "origin/" + r.cfg.BaseBranch
	if out, werr := r.git("worktree", "add", "-b", mergeBranch, mergeWorktree, startPoint); werr != nil {
		// Fallback: try without origin/ prefix.
		if out2, werr2 := r.git("worktree", "add", "-b", mergeBranch, mergeWorktree, r.cfg.BaseBranch); werr2 != nil {
			_ = out2
			r.cleanupWorktrees(results)
			return batchResult, fmt.Errorf("create consolidation worktree: %s: %w", strings.TrimSpace(out), werr)
		}
	}

	var mergeErrors []string
	for _, res := range results {
		if res.Error != nil || res.Branch == "" {
			continue
		}
		progressFn(fmt.Sprintf("Merging %s...", res.Subtask.Name))
		out, merr := r.gitIn(mergeWorktree,
			"merge", "--no-ff", res.Branch,
			"-m", fmt.Sprintf("batch: merge subtask %s\n\n%s", res.Subtask.Name, res.Subtask.Description),
		)
		if merr != nil {
			mergeErrors = append(mergeErrors, fmt.Sprintf("%s: %s", res.Subtask.Name, strings.TrimSpace(out)))
			// Abort the conflicted merge so the branch stays usable.
			if _, aerr := r.gitIn(mergeWorktree, "merge", "--abort"); aerr != nil {
				logger.Debugf("[batch] merge --abort in %s: %v", mergeWorktree, aerr)
			}
		}
	}

	batchResult.MergeBranch = mergeBranch
	batchResult.MergeErrors = mergeErrors

	// 6. Push the consolidation branch.
	if out, perr := r.gitIn(mergeWorktree, "push", "-u", "origin", mergeBranch); perr != nil {
		r.cleanupWorktrees(results)
		_, _ = r.git("worktree", "remove", "--force", mergeWorktree)
		return batchResult, fmt.Errorf("push consolidation branch: %w\n%s", perr, strings.TrimSpace(out))
	}

	// 7. Create the PR.
	if r.cfg.Repo != "" {
		progressFn("Creating PR...")
		prURL, prerr := r.createPR(ctx, task, batchResult)
		if prerr != nil {
			progressFn(fmt.Sprintf("PR creation failed: %v", prerr))
		} else {
			batchResult.PRURL = prURL
			progressFn(fmt.Sprintf("PR created: %s", prURL))
		}
	}

	// 8. Clean up all worktrees (including consolidated).
	_, _ = r.git("worktree", "remove", "--force", mergeWorktree)
	r.cleanupWorktrees(results)

	return batchResult, nil
}

// runSubtask creates a git worktree for one subtask, runs the worker inside it,
// and commits any uncommitted changes before returning.
func (r *BatchRunner) runSubtask(ctx context.Context, batchID, worktreeBase string, st Subtask) SubtaskResult {
	res := SubtaskResult{Subtask: st}

	branch := fmt.Sprintf("batch/%s/%s", batchID, st.Name)
	worktreePath := filepath.Join(worktreeBase, st.Name)

	startPoint := "origin/" + r.cfg.BaseBranch
	if out, err := r.git("worktree", "add", "-b", branch, worktreePath, startPoint); err != nil {
		// Fallback: without origin/ prefix.
		if out2, err2 := r.git("worktree", "add", "-b", branch, worktreePath, r.cfg.BaseBranch); err2 != nil {
			res.Error = fmt.Errorf("create worktree: %s: %w", strings.TrimSpace(out), err)
			_ = out2
			return res
		}
	}
	res.Branch = branch
	res.Worktree = worktreePath

	// Run the AI worker in the isolated worktree.
	result, err := r.cfg.Adapter.Run(ctx, Request{
		Task:    st.Description,
		Model:   r.cfg.Model,
		WorkDir: worktreePath,
	})
	if err != nil {
		res.Error = fmt.Errorf("worker: %w", err)
		return res
	}
	res.Output = result.Content

	// Commit any changes the worker left uncommitted.
	if _, aerr := r.gitIn(worktreePath, "add", "-A"); aerr != nil {
		logger.Debugf("[batch] git add -A in %s: %v", st.Name, aerr)
	}
	cout, cerr := r.gitIn(worktreePath, "commit", "-m",
		fmt.Sprintf("batch: %s\n\n%s", st.Name, st.Description))
	if cerr != nil && !strings.Contains(cout, "nothing to commit") {
		logger.Debugf("[batch] commit in worktree %s: %s", st.Name, cout)
	}

	return res
}

// createPR creates a GitHub PR for the consolidation branch.
func (r *BatchRunner) createPR(ctx context.Context, task string, result *BatchResult) (string, error) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("## Batch Task\n\n%s\n\n", task))
	sb.WriteString(fmt.Sprintf("## Subtasks (%d)\n\n", len(result.Subtasks)))
	for _, res := range result.Subtasks {
		if res.Error != nil {
			sb.WriteString(fmt.Sprintf("- ❌ **%s**: %v\n", res.Subtask.Name, res.Error))
		} else {
			sb.WriteString(fmt.Sprintf("- ✅ **%s**: %s\n", res.Subtask.Name, res.Subtask.Description))
		}
	}
	if len(result.MergeErrors) > 0 {
		sb.WriteString("\n## Merge Conflicts\n\n")
		sb.WriteString("The following subtasks had merge conflicts and were skipped from the consolidation:\n\n")
		for _, me := range result.MergeErrors {
			sb.WriteString(fmt.Sprintf("- %s\n", me))
		}
	}
	sb.WriteString(fmt.Sprintf("\n---\n_Batch ID: `%s`_\n", result.BatchID))

	title := fmt.Sprintf("batch: %s", batchTruncate(task, 60))

	cmd := exec.CommandContext(ctx, "gh",
		"pr", "create",
		"--repo", r.cfg.Repo,
		"--title", title,
		"--body", sb.String(),
		"--base", r.cfg.BaseBranch,
		"--head", result.MergeBranch,
	)
	if r.cfg.WorkDir != "" {
		cmd.Dir = r.cfg.WorkDir
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("gh pr create: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// cleanupWorktrees removes git worktrees for all completed subtask results.
func (r *BatchRunner) cleanupWorktrees(results []SubtaskResult) {
	for _, res := range results {
		if res.Worktree == "" {
			continue
		}
		if _, err := r.git("worktree", "remove", "--force", res.Worktree); err != nil {
			logger.Debugf("[batch] cleanup worktree %s: %v", res.Worktree, err)
		}
	}
}

// git runs a git command in WorkDir and returns combined output.
func (r *BatchRunner) git(args ...string) (string, error) {
	return r.gitIn(r.cfg.WorkDir, args...)
}

// gitIn runs a git command in the specified directory and returns combined output.
func (r *BatchRunner) gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// batchTruncate truncates s to at most max runes, adding "..." if truncated.
func batchTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
