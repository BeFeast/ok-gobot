package batch

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"ok-gobot/internal/worker"
)

// Config controls how the batch runner operates.
type Config struct {
	// Parallelism is the maximum number of concurrent workers. Default: 5.
	Parallelism int
	// RepoRoot is the absolute path to the git repository root.
	RepoRoot string
	// BaseBranch is the branch new worktrees are forked from. Default: "main".
	BaseBranch string
	// WorktreeParent is the parent directory for worktree paths.
	// If empty, os.TempDir() is used.
	WorktreeParent string
	// WorkerModel is forwarded to worker backends and reported in preflight.
	WorkerModel string
	// Progress is called after each subtask completes (may be nil).
	Progress func(SubtaskResult)
	// Out receives log/status lines (may be nil; falls back to os.Stderr).
	Out io.Writer
}

// SubtaskResult is the outcome for one subtask.
type SubtaskResult struct {
	Index   int
	Subtask Subtask
	Branch  string
	WorkDir string
	Output  string
	Error   error
}

// MergeResult is the outcome of the merge phase.
type MergeResult struct {
	MergeBranch string
	Merged      []string // branches successfully merged
	Conflicts   []string // branches with merge conflicts
}

// Run executes each subtask in a dedicated git worktree using the provided
// adapter. Workers run concurrently up to cfg.Parallelism.
// Returns per-subtask results; a non-nil top-level error means setup failed.
func Run(ctx context.Context, cfg Config, subtasks []Subtask, adapter worker.Adapter) ([]SubtaskResult, error) {
	if cfg.Parallelism <= 0 {
		cfg.Parallelism = 5
	}
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = "main"
	}
	if cfg.RepoRoot == "" {
		root, err := gitRepoRoot()
		if err != nil {
			return nil, fmt.Errorf("failed to detect git repo root: %w", err)
		}
		cfg.RepoRoot = root
	}
	out := cfg.Out
	if out == nil {
		out = os.Stderr
	}

	results := make([]SubtaskResult, len(subtasks))
	sem := make(chan struct{}, cfg.Parallelism)
	var wg sync.WaitGroup

	for i, st := range subtasks {
		wg.Add(1)
		go func(idx int, subtask Subtask) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			res := runSubtask(ctx, cfg, idx, subtask, adapter, out)
			results[idx] = res

			if cfg.Progress != nil {
				cfg.Progress(res)
			}
		}(i, st)
	}

	wg.Wait()
	return results, nil
}

// MergeBranches merges all successful subtask branches into a new branch.
// Returns the merge result (conflicts are non-fatal; they are reported).
func MergeBranches(cfg Config, batchSlug string, results []SubtaskResult) (MergeResult, error) {
	if cfg.RepoRoot == "" {
		root, err := gitRepoRoot()
		if err != nil {
			return MergeResult{}, fmt.Errorf("failed to detect git repo root: %w", err)
		}
		cfg.RepoRoot = root
	}
	if cfg.BaseBranch == "" {
		cfg.BaseBranch = "main"
	}
	out := cfg.Out
	if out == nil {
		out = os.Stderr
	}

	mergeBranch := fmt.Sprintf("batch/%s-merged", batchSlug)

	// Create the merge branch from base.
	if err := gitCmd(cfg.RepoRoot, out, "checkout", "-b", mergeBranch, cfg.BaseBranch); err != nil {
		// Branch may already exist; try checking it out.
		if err2 := gitCmd(cfg.RepoRoot, out, "checkout", mergeBranch); err2 != nil {
			return MergeResult{}, fmt.Errorf("failed to create merge branch %s: %w", mergeBranch, err)
		}
	}

	mr := MergeResult{MergeBranch: mergeBranch}

	for _, res := range results {
		if res.Error != nil || res.Branch == "" {
			continue
		}

		fmt.Fprintf(out, "[batch] merging branch %s\n", res.Branch)
		if err := gitCmd(cfg.RepoRoot, out, "merge", "--no-ff", "--no-edit", res.Branch); err != nil {
			fmt.Fprintf(out, "[batch] conflict merging %s: %v\n", res.Branch, err)
			mr.Conflicts = append(mr.Conflicts, res.Branch)
			// Abort the failed merge so the repo stays clean.
			_ = gitCmd(cfg.RepoRoot, out, "merge", "--abort")
			continue
		}
		mr.Merged = append(mr.Merged, res.Branch)
	}

	return mr, nil
}

// CreatePR uses the gh CLI to open a pull request from mergeBranch to base.
func CreatePR(repoRoot, mergeBranch, baseBranch, title, body string, out io.Writer) (string, error) {
	if out == nil {
		out = os.Stderr
	}

	// Push merge branch first.
	if err := gitCmd(repoRoot, out, "push", "--force-with-lease", "origin", mergeBranch); err != nil {
		return "", fmt.Errorf("failed to push merge branch: %w", err)
	}

	args := []string{
		"pr", "create",
		"--title", title,
		"--body", body,
		"--base", baseBranch,
		"--head", mergeBranch,
	}

	cmd := exec.Command("gh", args...)
	cmd.Dir = repoRoot
	cmd.Stdout = out
	cmd.Stderr = out

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("gh pr create failed: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// CleanWorktrees removes all worktrees created by Run.
func CleanWorktrees(repoRoot string, results []SubtaskResult, out io.Writer) {
	if out == nil {
		out = os.Stderr
	}
	for _, res := range results {
		if res.WorkDir == "" {
			continue
		}
		if err := gitCmd(repoRoot, out, "worktree", "remove", "--force", res.WorkDir); err != nil {
			log.Printf("[batch] failed to remove worktree %s: %v", res.WorkDir, err)
		}
	}
}

// --- internal helpers ---

func runSubtask(ctx context.Context, cfg Config, idx int, subtask Subtask, adapter worker.Adapter, out io.Writer) SubtaskResult {
	res := SubtaskResult{
		Index:   idx,
		Subtask: subtask,
	}

	slug := slugify(subtask.Title)
	branch := fmt.Sprintf("batch/%s-%d-%s", slugify(cfg.BaseBranch), idx, slug)
	res.Branch = branch

	// Create the worktree directory.
	parent := cfg.WorktreeParent
	if parent == "" {
		parent = os.TempDir()
	}
	workDir := filepath.Join(parent, fmt.Sprintf("ok-gobot-batch-%d-%s", idx, slug))
	res.WorkDir = workDir

	fmt.Fprintf(out, "[batch] subtask %d: %s — branch %s\n", idx, subtask.Title, branch)

	// Create branch from base and attach a worktree.
	if err := gitCmd(cfg.RepoRoot, out, "worktree", "add", "-b", branch, workDir, cfg.BaseBranch); err != nil {
		res.Error = fmt.Errorf("failed to create worktree: %w", err)
		fmt.Fprintf(out, "[batch] subtask %d error: %v\n", idx, res.Error)
		return res
	}

	// Run the worker adapter.
	req := worker.Request{
		Task:    subtask.Description,
		Model:   cfg.WorkerModel,
		WorkDir: workDir,
	}
	if report, ok := worker.RunAdapterPreflight(ctx, adapter, req); ok {
		fmt.Fprintf(out, "[batch] subtask %d preflight: %s\n", idx, report.Summary())
		if !report.OK {
			res.Error = fmt.Errorf("worker preflight failed: %s", report.Summary())
			return res
		}
	}

	result, err := adapter.Run(ctx, req)
	if err != nil {
		res.Error = fmt.Errorf("worker failed: %w", err)
		fmt.Fprintf(out, "[batch] subtask %d error: %v\n", idx, res.Error)
		return res
	}

	res.Output = result.Content

	// Commit any changes made by the worker.
	_ = gitCmd(workDir, out, "add", "-A")
	msg := fmt.Sprintf("batch: %s\n\n%s", subtask.Title, subtask.Description)
	// Ignore error — there may be nothing to commit.
	_ = gitCmdRaw(workDir, "commit", "-m", msg)

	fmt.Fprintf(out, "[batch] subtask %d complete: %s\n", idx, subtask.Title)
	return res
}

// gitRepoRoot returns the top-level directory of the current git repo.
func gitRepoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitCmd runs a git command in dir, writing output to out.
func gitCmd(dir string, out io.Writer, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

// gitCmdRaw runs git silently (discards output), used for best-effort calls.
func gitCmdRaw(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

// slugify converts a string into a lowercase-hyphenated slug safe for branch names.
var nonAlphanumRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	return SlugifyExported(s)
}

// SlugifyExported converts a string into a lowercase-hyphenated slug safe for
// git branch names. The maximum length is 40 characters.
func SlugifyExported(s string) string {
	s = strings.ToLower(s)
	s = nonAlphanumRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}
