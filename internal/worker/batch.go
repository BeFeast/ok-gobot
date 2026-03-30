package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// BatchConfig configures the batch fan-out runner.
type BatchConfig struct {
	// MaxWorkers limits how many subtasks run in parallel. Defaults to 5.
	MaxWorkers int
	// WorkDir is the root git repository directory. Defaults to the current directory.
	WorkDir string
	// WorktreeBaseDir is the parent directory for temporary worktrees. Defaults to a system temp dir.
	WorktreeBaseDir string
	// CleanupWorktrees removes temporary worktrees after all subtasks complete.
	CleanupWorktrees bool
}

// Subtask is one parallelizable unit of work decomposed from a larger task.
type Subtask struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

// SubtaskResult holds the outcome of running one subtask.
type SubtaskResult struct {
	Subtask Subtask
	WorkDir string
	Branch  string
	Output  string
	Error   error
}

// ProgressFunc receives human-readable status messages during batch execution.
type ProgressFunc func(msg string)

// BatchRunner orchestrates parallel subtask fan-out across git worktrees.
type BatchRunner struct {
	cfg      BatchConfig
	adapter  Adapter
	progress ProgressFunc
}

// NewBatchRunner returns a BatchRunner with the given configuration.
// A nil progress function is replaced with a no-op.
func NewBatchRunner(cfg BatchConfig, adapter Adapter, progress ProgressFunc) *BatchRunner {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 5
	}
	if progress == nil {
		progress = func(string) {}
	}
	return &BatchRunner{cfg: cfg, adapter: adapter, progress: progress}
}

// AICompleteFunc is a single-turn AI completion function.
// The caller wraps their AI client to match this signature.
type AICompleteFunc func(ctx context.Context, userPrompt string) (string, error)

// DecomposeTask calls the AI completion function to split a task description into
// independent subtasks. The returned slice contains between 2 and 10 subtasks.
func DecomposeTask(ctx context.Context, complete AICompleteFunc, task string) ([]Subtask, error) {
	prompt := fmt.Sprintf(`You are a task decomposition assistant. Split the following coding task into independent, parallelizable subtasks that can run concurrently in separate git worktrees.

Task: %s

Rules:
- Each subtask must be fully independent (no dependency on another subtask's output)
- 2–10 subtasks total
- Each subtask should be concrete and bounded
- Subtask IDs must be short alphanumeric slugs, e.g. "refactor-auth" or "add-tests-api"

Respond ONLY with a JSON array (no markdown fences, no explanation):
[
  {"id": "subtask-slug", "description": "Specific description of what to do"},
  ...
]`, task)

	response, err := complete(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("task decomposition: %w", err)
	}

	jsonStr := extractJSONArray(response)

	var subtasks []Subtask
	if err := json.Unmarshal([]byte(jsonStr), &subtasks); err != nil {
		return nil, fmt.Errorf("parse decomposition response: %w\nresponse: %s", err, response)
	}

	if len(subtasks) == 0 {
		return nil, fmt.Errorf("decomposition returned no subtasks")
	}

	return subtasks, nil
}

// Run executes all subtasks in parallel (up to MaxWorkers at once) and returns results.
// batchID is a short unique string used for branch and worktree naming.
func (r *BatchRunner) Run(ctx context.Context, subtasks []Subtask, batchID string) []SubtaskResult {
	worktreeBase := r.cfg.WorktreeBaseDir
	if worktreeBase == "" {
		dir, err := os.MkdirTemp("", fmt.Sprintf("ok-gobot-batch-%s-*", batchID))
		if err != nil {
			dir = fmt.Sprintf(".batch-%s", batchID)
		}
		worktreeBase = dir
	}

	results := make([]SubtaskResult, len(subtasks))
	sem := make(chan struct{}, r.cfg.MaxWorkers)
	var wg sync.WaitGroup

	for i, st := range subtasks {
		wg.Add(1)
		go func(idx int, subtask Subtask) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			r.progress(fmt.Sprintf("[batch] starting subtask %q", subtask.ID))
			res := r.runSubtask(ctx, subtask, worktreeBase, batchID)
			results[idx] = res
			if res.Error != nil {
				r.progress(fmt.Sprintf("[batch] subtask %q failed: %v", subtask.ID, res.Error))
			} else {
				r.progress(fmt.Sprintf("[batch] subtask %q done", subtask.ID))
			}
		}(i, st)
	}

	wg.Wait()
	return results
}

// runSubtask creates a worktree, runs the adapter, and returns the result.
func (r *BatchRunner) runSubtask(ctx context.Context, st Subtask, worktreeBase, batchID string) SubtaskResult {
	branch := fmt.Sprintf("batch/%s/%s", batchID, st.ID)
	worktreeDir := filepath.Join(worktreeBase, st.ID)

	workDir := r.cfg.WorkDir
	if workDir == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return SubtaskResult{Subtask: st, Error: fmt.Errorf("get working directory: %w", err)}
		}
	}

	if err := createWorktree(workDir, worktreeDir, branch); err != nil {
		return SubtaskResult{Subtask: st, Error: err}
	}

	if r.cfg.CleanupWorktrees {
		defer func() {
			_ = removeWorktree(workDir, worktreeDir)
		}()
	}

	req := Request{
		Task:    st.Description,
		WorkDir: worktreeDir,
	}

	result, err := r.adapter.Run(ctx, req)
	if err != nil {
		return SubtaskResult{
			Subtask: st,
			WorkDir: worktreeDir,
			Branch:  branch,
			Error:   err,
		}
	}

	return SubtaskResult{
		Subtask: st,
		WorkDir: worktreeDir,
		Branch:  branch,
		Output:  result.Content,
	}
}

// createWorktree runs `git worktree add -b <branch> <dir> HEAD` in repoDir.
func createWorktree(repoDir, worktreeDir, branch string) error {
	cmd := exec.Command("git", "worktree", "add", "-b", branch, worktreeDir, "HEAD")
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// removeWorktree removes a worktree and prunes its reference from repoDir.
func removeWorktree(repoDir, worktreeDir string) error {
	cmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %w\n%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// extractJSONArray extracts the first JSON array from a string, stripping markdown fences.
func extractJSONArray(s string) string {
	// Strip markdown code fences
	re := regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")
	if m := re.FindStringSubmatch(s); len(m) > 1 {
		s = m[1]
	}
	// Find first [ ... ]
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start != -1 && end != -1 && start <= end {
		return s[start : end+1]
	}
	return strings.TrimSpace(s)
}

// NewBatchID returns a short unique batch identifier based on current time.
func NewBatchID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000)
}
