package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/batch"
	"ok-gobot/internal/config"
	"ok-gobot/internal/worker"
)

func newBatchCommand(cfg *config.Config) *cobra.Command {
	var (
		parallel       int
		workerType     string
		workerModel    string
		baseBranch     string
		noPR           bool
		worktreeParent string
		repoRoot       string
	)

	cmd := &cobra.Command{
		Use:   "batch <task description>",
		Short: "Decompose a task and execute subtasks in parallel using AI workers",
		Long: `Decompose a large task into independent subtasks and execute each one in
a separate git worktree using an AI worker (claude, codex, or droid).

The task description is analysed by the configured AI provider, which produces
a list of independent subtasks. Each subtask runs in its own git worktree
branched from --base. Results are merged into a single branch and (optionally)
a PR is opened.

Examples:
  ok-gobot batch "add unit tests for all functions in internal/storage"
  ok-gobot batch --parallel 3 --worker codex "update import paths from v1 to v2"
  ok-gobot batch --no-pr "rename Foo to Bar across all Go files"
`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task := strings.Join(args, " ")
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "[batch] task: %s\n", task)
			fmt.Fprintf(out, "[batch] worker: %s  parallel: %d  base: %s\n", workerType, parallel, baseBranch)

			// --- 1. Build AI client for decomposition ---
			aiCfg := ai.ProviderConfig{
				Name:    cfg.AI.Provider,
				APIKey:  cfg.AI.APIKey,
				BaseURL: cfg.AI.BaseURL,
				Model:   cfg.AI.Model,
			}
			aiClient, err := ai.NewClient(aiCfg)
			if err != nil {
				return fmt.Errorf("failed to create AI client: %w", err)
			}

			// --- 2. Decompose task into subtasks ---
			fmt.Fprintf(out, "[batch] decomposing task using %s/%s…\n", cfg.AI.Provider, cfg.AI.Model)
			subtasks, err := batch.DecomposeTask(ctx, aiClient, task, parallel*2)
			if err != nil {
				return fmt.Errorf("failed to decompose task: %w", err)
			}

			fmt.Fprintf(out, "[batch] %d subtasks:\n", len(subtasks))
			for i, st := range subtasks {
				fmt.Fprintf(out, "  %d. %s\n", i+1, st.Title)
			}

			// --- 3. Build worker adapter ---
			adapter, err := buildAdapter(workerType, workerModel, cfg)
			if err != nil {
				return fmt.Errorf("failed to build worker adapter: %w", err)
			}

			// --- 4. Run subtasks in parallel ---
			fmt.Fprintf(out, "[batch] starting %d workers (max parallel: %d)…\n", len(subtasks), parallel)

			runCfg := batch.Config{
				Parallelism:    parallel,
				RepoRoot:       repoRoot,
				BaseBranch:     baseBranch,
				WorktreeParent: worktreeParent,
				Out:            out,
				Progress: func(res batch.SubtaskResult) {
					if res.Error != nil {
						fmt.Fprintf(out, "[batch] ✗ subtask %d failed: %v\n", res.Index, res.Error)
					} else {
						fmt.Fprintf(out, "[batch] ✓ subtask %d done: %s\n", res.Index, res.Subtask.Title)
					}
				},
			}

			results, err := batch.Run(ctx, runCfg, subtasks, adapter)
			if err != nil {
				return fmt.Errorf("batch run failed: %w", err)
			}

			// --- 5. Report individual results ---
			var succeeded, failed int
			for _, r := range results {
				if r.Error != nil {
					failed++
				} else {
					succeeded++
				}
			}
			fmt.Fprintf(out, "[batch] results: %d succeeded, %d failed\n", succeeded, failed)

			if succeeded == 0 {
				return fmt.Errorf("all subtasks failed — no changes to merge")
			}

			// --- 6. Merge branches ---
			batchSlug := slugFromTask(task)
			fmt.Fprintf(out, "[batch] merging successful branches…\n")

			mr, err := batch.MergeBranches(runCfg, batchSlug, results)
			if err != nil {
				return fmt.Errorf("merge phase failed: %w", err)
			}

			fmt.Fprintf(out, "[batch] merged %d branch(es), %d conflict(s)\n", len(mr.Merged), len(mr.Conflicts))
			if len(mr.Conflicts) > 0 {
				fmt.Fprintf(out, "[batch] conflicting branches (resolve manually):\n")
				for _, b := range mr.Conflicts {
					fmt.Fprintf(out, "  - %s\n", b)
				}
			}

			// --- 7. Create PR ---
			if noPR {
				fmt.Fprintf(out, "[batch] --no-pr set; skipping PR creation. Merge branch: %s\n", mr.MergeBranch)
			} else if len(mr.Merged) > 0 {
				title := fmt.Sprintf("batch: %s", truncateString(task, 60))
				body := buildPRBody(task, subtasks, results, mr)

				prURL, err := batch.CreatePR(runCfg.RepoRoot, mr.MergeBranch, baseBranch, title, body, out)
				if err != nil {
					fmt.Fprintf(out, "[batch] warning: failed to create PR: %v\n", err)
					fmt.Fprintf(out, "[batch] merge branch ready: %s\n", mr.MergeBranch)
				} else {
					fmt.Fprintf(out, "[batch] PR created: %s\n", prURL)
				}
			} else {
				fmt.Fprintf(out, "[batch] no branches merged successfully; skipping PR\n")
			}

			// --- 8. Clean up worktrees ---
			fmt.Fprintf(out, "[batch] cleaning up worktrees…\n")
			batch.CleanWorktrees(runCfg.RepoRoot, results, out)

			fmt.Fprintf(out, "[batch] done.\n")
			return nil
		},
	}

	cmd.Flags().IntVar(&parallel, "parallel", 5, "maximum number of concurrent workers")
	cmd.Flags().StringVar(&workerType, "worker", "claude", "worker type: claude, codex, droid")
	cmd.Flags().StringVar(&workerModel, "model", "", "model override for the worker (default: worker binary default)")
	cmd.Flags().StringVar(&baseBranch, "base", "main", "base branch to fork worktrees from")
	cmd.Flags().BoolVar(&noPR, "no-pr", false, "skip PR creation after merging")
	cmd.Flags().StringVar(&worktreeParent, "worktree-dir", "", "parent directory for worktrees (default: os temp dir)")
	cmd.Flags().StringVar(&repoRoot, "repo", "", "path to git repo root (default: auto-detected)")

	return cmd
}

// buildAdapter constructs the appropriate worker.Adapter based on the worker type.
func buildAdapter(workerType, model string, cfg *config.Config) (worker.Adapter, error) {
	switch strings.ToLower(workerType) {
	case "claude":
		claudeCfg := worker.ClaudeConfig{}
		if model != "" {
			// model passed via --model is forwarded in the Request, not the config
		}
		return worker.NewClaudeAdapter(claudeCfg), nil

	case "codex":
		codexCfg := worker.CodexConfig{}
		return worker.NewCodexAdapter(codexCfg), nil

	case "droid":
		droidCfg := worker.DroidConfig{
			BinaryPath: cfg.AI.Droid.BinaryPath,
			AutoLevel:  cfg.AI.Droid.AutoLevel,
			WorkDir:    cfg.AI.Droid.WorkDir,
		}
		return worker.NewDroidAdapter(droidCfg), nil

	default:
		return nil, fmt.Errorf("unknown worker type %q (supported: claude, codex, droid)", workerType)
	}
}

// slugFromTask creates a short slug from the task description for branch naming.
func slugFromTask(task string) string {
	// Use timestamp to ensure uniqueness.
	ts := time.Now().Format("0102-1504")
	slug := batch.SlugifyExported(task)
	if len(slug) > 30 {
		slug = slug[:30]
	}
	return fmt.Sprintf("%s-%s", ts, slug)
}

// truncateString truncates s to at most n characters, appending "…" if trimmed.
func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// buildPRBody composes a markdown PR description summarising the batch run.
func buildPRBody(task string, subtasks []batch.Subtask, results []batch.SubtaskResult, mr batch.MergeResult) string {
	var sb strings.Builder

	sb.WriteString("## Batch task\n\n")
	sb.WriteString("> ")
	sb.WriteString(task)
	sb.WriteString("\n\n")

	sb.WriteString("## Subtasks\n\n")
	for i, st := range subtasks {
		var status string
		if i < len(results) {
			if results[i].Error != nil {
				status = "✗ failed"
			} else {
				status = "✓ done"
			}
		}
		fmt.Fprintf(&sb, "- **%s** — %s %s\n", st.Title, st.Description, status)
	}

	if len(mr.Conflicts) > 0 {
		sb.WriteString("\n## Merge conflicts\n\nThe following branches had conflicts and were skipped:\n\n")
		for _, b := range mr.Conflicts {
			fmt.Fprintf(&sb, "- `%s`\n", b)
		}
	}

	sb.WriteString("\n## Generated by\n\n`ok-gobot batch`\n")

	hostname, _ := os.Hostname()
	if hostname != "" {
		fmt.Fprintf(&sb, "Host: %s\n", hostname)
	}

	return sb.String()
}
