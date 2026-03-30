package cli

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/worker"
)

func newBatchCommand(cfg *config.Config) *cobra.Command {
	var (
		maxParallel int
		baseBranch  string
		repo        string
		model       string
		workDir     string
		noPR        bool
		dryRun      bool
		adapterName string
	)

	cmd := &cobra.Command{
		Use:   "batch '<task description>'",
		Short: "Decompose a task and run subtasks in parallel git worktrees",
		Long: `Batch fan-out: analyze a task, split it into independent subtasks, run each in
an isolated git worktree, then consolidate results into a single PR.

Examples:
  ok-gobot batch 'add unit tests for all functions in internal/worker/'
  ok-gobot batch 'rename Config to Configuration across all packages' --max-parallel 3
  ok-gobot batch 'update import paths from old/pkg to new/pkg' --dry-run
  ok-gobot batch 'fix lint errors across the repo' --no-pr`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task := args[0]

			// Resolve working directory.
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			// Auto-detect GitHub repo from git remote if not specified.
			if repo == "" && !noPR {
				repo = detectGitHubRepo(workDir)
			}

			// Use model from config if not specified.
			if model == "" {
				model = cfg.AI.Model
			}

			// Build the adapter.
			a, err := buildBatchAdapter(cfg, adapterName, workDir)
			if err != nil {
				return fmt.Errorf("build adapter: %w", err)
			}

			batchCfg := worker.BatchConfig{
				MaxParallel: maxParallel,
				WorkDir:     workDir,
				Adapter:     a,
				Model:       model,
				BaseBranch:  baseBranch,
				Repo:        repo,
				SkipPR:      noPR,
			}
			runner := worker.NewBatchRunner(batchCfg)

			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "Task: %s\n\nDecomposing (dry run)...\n", task)
				subtasks, err := runner.Decompose(cmd.Context(), task)
				if err != nil {
					return fmt.Errorf("decompose: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "\nSubtasks (%d):\n", len(subtasks))
				for i, st := range subtasks {
					fmt.Fprintf(cmd.OutOrStdout(), "  %d. [%s] %s\n", i+1, st.Name, st.Description)
				}
				return nil
			}

			out := cmd.OutOrStdout()
			result, err := runner.Run(cmd.Context(), task, func(msg string) {
				fmt.Fprintln(out, msg)
			})
			if err != nil {
				return fmt.Errorf("batch run: %w", err)
			}

			// Summary.
			fmt.Fprintln(out, "\n--- Batch Summary ---")
			fmt.Fprintf(out, "Batch ID:  %s\n", result.BatchID)

			succeeded := 0
			for _, r := range result.Subtasks {
				if r.Error == nil {
					succeeded++
				}
			}
			fmt.Fprintf(out, "Subtasks:  %d total, %d succeeded\n", len(result.Subtasks), succeeded)

			if result.MergeBranch != "" {
				fmt.Fprintf(out, "Branch:    %s\n", result.MergeBranch)
			}
			if result.PRURL != "" {
				fmt.Fprintf(out, "PR:        %s\n", result.PRURL)
			}
			if len(result.MergeErrors) > 0 {
				fmt.Fprintln(out, "\nMerge conflicts (subtasks skipped from consolidation):")
				for _, me := range result.MergeErrors {
					fmt.Fprintf(out, "  - %s\n", me)
				}
			}

			fmt.Fprintln(out, "\nSubtasks:")
			for _, r := range result.Subtasks {
				status := "✓"
				detail := ""
				if r.Error != nil {
					status = "✗"
					detail = fmt.Sprintf(": %v", r.Error)
				}
				fmt.Fprintf(out, "  %s [%s] %s%s\n", status, r.Subtask.Name, r.Subtask.Description, detail)
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&maxParallel, "max-parallel", 5, "maximum number of parallel workers")
	cmd.Flags().StringVar(&baseBranch, "base-branch", "main", "base git branch to create worktrees from")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repo (owner/name) for PR creation; auto-detected from git remote if empty")
	cmd.Flags().StringVar(&model, "model", "", "AI model to use (defaults to config ai.model)")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "git repository root (defaults to current directory)")
	cmd.Flags().BoolVar(&noPR, "no-pr", false, "skip automatic PR creation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "decompose only — print subtasks without running workers")
	cmd.Flags().StringVar(&adapterName, "adapter", "", "worker adapter: claude, codex, droid (default: claude, or droid if ai.provider=droid)")

	return cmd
}

// buildBatchAdapter creates the appropriate worker adapter from config and flags.
func buildBatchAdapter(cfg *config.Config, adapterName, workDir string) (worker.Adapter, error) {
	if adapterName == "" {
		switch cfg.AI.Provider {
		case "droid":
			adapterName = "droid"
		default:
			adapterName = "claude"
		}
	}
	switch adapterName {
	case "claude":
		return worker.NewClaudeAdapter(worker.ClaudeConfig{
			WorkDir: workDir,
		}), nil
	case "codex":
		return worker.NewCodexAdapter(worker.CodexConfig{
			WorkDir: workDir,
		}), nil
	case "droid":
		return worker.NewDroidAdapter(worker.DroidConfig{
			BinaryPath: cfg.AI.Droid.BinaryPath,
			AutoLevel:  cfg.AI.Droid.AutoLevel,
			WorkDir:    workDir,
		}), nil
	default:
		return nil, fmt.Errorf("unknown adapter %q (choose claude, codex, or droid)", adapterName)
	}
}

// detectGitHubRepo infers the GitHub "owner/repo" from the git remote URL.
func detectGitHubRepo(dir string) string {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	remote := strings.TrimSpace(string(out))

	// SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(remote, "git@github.com:") {
		repo := strings.TrimPrefix(remote, "git@github.com:")
		return strings.TrimSuffix(repo, ".git")
	}
	// HTTPS: https://github.com/owner/repo.git
	if idx := strings.Index(remote, "github.com/"); idx >= 0 {
		repo := remote[idx+len("github.com/"):]
		return strings.TrimSuffix(repo, ".git")
	}
	return ""
}
