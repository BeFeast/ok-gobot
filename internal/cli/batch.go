package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
	"ok-gobot/internal/worker"
)

func newBatchCommand(cfg *config.Config) *cobra.Command {
	var (
		maxWorkers int
		workerType string
		model      string
		dryRun     bool
		noCleanup  bool
		workDir    string
		createPR   bool
	)

	cmd := &cobra.Command{
		Use:   "batch '<task description>'",
		Short: "Fan-out a task into parallel subtasks with automatic git worktrees",
		Long: `Decompose a high-level task into independent subtasks and execute each
in a dedicated git worktree in parallel. Results are reported and optionally
consolidated into a pull request.

The AI provider configured in ok-gobot (ai.provider / ai.api_key) is used for
task decomposition. Each subtask is executed by the selected worker (--worker).

Examples:
  ok-gobot batch 'Add unit tests for all untested functions in internal/worker/'
  ok-gobot batch --max-workers 3 --worker codex 'Update all import paths from foo to bar'
  ok-gobot batch --dry-run 'Rename all occurrences of OldName to NewName across modules'
  ok-gobot batch --pr 'Add godoc comments to exported types in internal/storage/'`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task := args[0]
			ctx := cmd.Context()

			// Resolve working directory.
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			// Build AI client for task decomposition.
			aiCfg := ai.ProviderConfig{
				Name:    cfg.AI.Provider,
				APIKey:  cfg.AI.APIKey,
				Model:   cfg.AI.Model,
				BaseURL: cfg.AI.BaseURL,
			}
			if model != "" {
				aiCfg.Model = model
			}
			aiClient, err := ai.NewClient(aiCfg)
			if err != nil {
				return fmt.Errorf("create AI client: %w", err)
			}

			// Decompose task into subtasks.
			fmt.Fprintf(cmd.OutOrStdout(), "Decomposing task with %s/%s...\n", cfg.AI.Provider, aiCfg.Model)
			completer := func(ctx context.Context, userPrompt string) (string, error) {
				return aiClient.Complete(ctx, []ai.Message{{Role: "user", Content: userPrompt}})
			}
			subtasks, err := worker.DecomposeTask(ctx, completer, task)
			if err != nil {
				return fmt.Errorf("decompose task: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Decomposed into %d subtasks:\n", len(subtasks))
			for i, st := range subtasks {
				fmt.Fprintf(cmd.OutOrStdout(), "  %d. [%s] %s\n", i+1, st.ID, st.Description)
			}

			if dryRun {
				fmt.Fprintln(cmd.OutOrStdout(), "\nDry-run: skipping execution.")
				return nil
			}

			// Build worker adapter.
			var adapter worker.Adapter
			switch strings.ToLower(workerType) {
			case "codex":
				adapter = worker.NewCodexAdapter(worker.CodexConfig{WorkDir: workDir})
			case "droid":
				adapter = worker.NewDroidAdapter(worker.DroidConfig{
					BinaryPath: cfg.AI.Droid.BinaryPath,
					AutoLevel:  cfg.AI.Droid.AutoLevel,
					WorkDir:    workDir,
				})
			default: // "claude"
				adapter = worker.NewClaudeAdapter(worker.ClaudeConfig{WorkDir: workDir})
			}

			batchID := worker.NewBatchID()
			batchCfg := worker.BatchConfig{
				MaxWorkers:       maxWorkers,
				WorkDir:          workDir,
				CleanupWorktrees: !noCleanup,
			}

			progress := func(msg string) {
				fmt.Fprintln(cmd.OutOrStdout(), msg)
			}

			runner := worker.NewBatchRunner(batchCfg, adapter, progress)

			fmt.Fprintf(cmd.OutOrStdout(), "\nStarting batch %s (%d subtasks, max %d parallel)...\n",
				batchID, len(subtasks), maxWorkers)

			results := runner.Run(ctx, subtasks, batchID)

			// Summarize results.
			var succeeded, failed int
			var successBranches []string
			fmt.Fprintln(cmd.OutOrStdout(), "\n--- Batch Results ---")
			for _, r := range results {
				if r.Error != nil {
					failed++
					fmt.Fprintf(cmd.OutOrStdout(), "  FAIL  [%s] %v\n", r.Subtask.ID, r.Error)
				} else {
					succeeded++
					successBranches = append(successBranches, r.Branch)
					fmt.Fprintf(cmd.OutOrStdout(), "  OK    [%s] branch: %s\n", r.Subtask.ID, r.Branch)
					if r.Output != "" {
						fmt.Fprintf(cmd.OutOrStdout(), "        %s\n", truncate(r.Output, 120))
					}
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\n%d/%d subtasks succeeded.\n", succeeded, len(results))

			if createPR && len(successBranches) > 0 {
				if err := consolidatePR(workDir, batchID, task, successBranches, cmd.OutOrStdout()); err != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "Warning: failed to create consolidated PR: %v\n", err)
				}
			} else if len(successBranches) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "\nSuccessful branches (use --pr to create a consolidated PR):\n")
				for _, b := range successBranches {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", b)
				}
			}

			if failed > 0 {
				return fmt.Errorf("%d subtask(s) failed", failed)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&maxWorkers, "max-workers", 5, "maximum number of parallel workers (default 5)")
	cmd.Flags().StringVar(&workerType, "worker", "claude", "worker type: claude, codex, droid")
	cmd.Flags().StringVar(&model, "model", "", "model override for decomposition (uses ai.model by default)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "decompose task but skip execution")
	cmd.Flags().BoolVar(&noCleanup, "no-cleanup", false, "keep temporary worktrees after execution")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "git repository root (defaults to current directory)")
	cmd.Flags().BoolVar(&createPR, "pr", false, "create a consolidated PR after all subtasks succeed")

	return cmd
}

// consolidatePR merges all successful subtask branches and creates a pull request.
func consolidatePR(repoDir, batchID, task string, branches []string, out interface{ Write([]byte) (int, error) }) error {
	consoleBranch := fmt.Sprintf("batch/%s/consolidated", batchID)

	// Create consolidated branch from HEAD.
	mkBranch := exec.Command("git", "checkout", "-b", consoleBranch)
	mkBranch.Dir = repoDir
	if output, err := mkBranch.CombinedOutput(); err != nil {
		return fmt.Errorf("create consolidated branch: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	var mergeWarnings []string
	for _, branch := range branches {
		merge := exec.Command("git", "merge", "--no-ff", "--no-edit", branch)
		merge.Dir = repoDir
		if output, err := merge.CombinedOutput(); err != nil {
			mergeWarnings = append(mergeWarnings,
				fmt.Sprintf("merge %s: %s", branch, strings.TrimSpace(string(output))))
		}
	}
	if len(mergeWarnings) > 0 {
		fmt.Fprintf(out, "Merge warnings:\n")
		for _, w := range mergeWarnings {
			fmt.Fprintf(out, "  %s\n", w)
		}
	}

	// Push consolidated branch.
	push := exec.Command("git", "push", "-u", "origin", consoleBranch)
	push.Dir = repoDir
	if output, err := push.CombinedOutput(); err != nil {
		return fmt.Errorf("push consolidated branch: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	// Build PR body.
	var bodyLines []string
	bodyLines = append(bodyLines, fmt.Sprintf("Automated batch consolidation for batch `%s`.", batchID))
	bodyLines = append(bodyLines, "")
	bodyLines = append(bodyLines, fmt.Sprintf("**Task:** %s", task))
	bodyLines = append(bodyLines, "")
	bodyLines = append(bodyLines, "**Merged subtask branches:**")
	for _, b := range branches {
		bodyLines = append(bodyLines, fmt.Sprintf("- `%s`", b))
	}
	prBody := strings.Join(bodyLines, "\n")
	prTitle := fmt.Sprintf("batch(%s): %s", batchID, truncate(task, 50))

	pr := exec.Command("gh", "pr", "create",
		"--title", prTitle,
		"--body", prBody,
		"--head", consoleBranch,
	)
	pr.Dir = repoDir
	output, err := pr.CombinedOutput()
	if err != nil {
		return fmt.Errorf("create PR: %w\n%s", err, strings.TrimSpace(string(output)))
	}

	fmt.Fprintf(out, "Consolidated PR created: %s\n", strings.TrimSpace(string(output)))
	return nil
}
