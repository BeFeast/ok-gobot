package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/worker"
)

// newWorkCommand implements `ok-gobot work '<task>'`.
// It auto-creates a git worktree + branch and spawns a worker (claude by default).
func newWorkCommand(cfg *config.Config) *cobra.Command {
	var (
		repoRoot  string
		baseDir   string
		model     string
		workerBin string
	)

	cmd := &cobra.Command{
		Use:   "work <task>",
		Short: "Create a worktree and spawn a worker for a task",
		Long: `Create a git worktree and branch for the given task, then spawn a worker agent
(claude by default) inside that worktree.

The worktree is tracked so you can list it with 'ok-gobot worktrees list' and
clean it up with 'ok-gobot worktrees cleanup'.

Example:
  ok-gobot work "fix the login bug"
  ok-gobot work --worker codex "add pagination to the API"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			task := strings.Join(args, " ")

			// Resolve repo root
			if repoRoot == "" {
				var err error
				repoRoot, err = worker.GitRepoRoot("")
				if err != nil {
					return fmt.Errorf("not inside a git repository; use --repo to specify one")
				}
			}

			// Resolve config defaults
			if baseDir == "" {
				baseDir = cfg.Worktree.BaseDir
			}
			if model == "" {
				model = cfg.Worktree.Model
			}
			if workerBin == "" {
				workerBin = cfg.Worktree.Worker
			}
			if workerBin == "" {
				workerBin = "claude"
			}

			mgr, err := worker.DefaultWorktreeManager()
			if err != nil {
				return err
			}

			fmt.Printf("Creating worktree for: %s\n", task)
			entry, err := mgr.CreateWorktree(repoRoot, baseDir, task)
			if err != nil {
				return fmt.Errorf("failed to create worktree: %w", err)
			}

			fmt.Printf("  Branch:    %s\n", entry.Branch)
			fmt.Printf("  Path:      %s\n", entry.Path)
			fmt.Printf("  ID:        %s\n", entry.ID)
			fmt.Println()

			// Build worker command
			workerArgs := buildWorkerArgs(workerBin, model, task)
			workerCmd := exec.CommandContext(cmd.Context(), workerBin, workerArgs...)
			workerCmd.Dir = entry.Path
			workerCmd.Stdin = os.Stdin

			// Stream stdout and stderr
			stdout, err := workerCmd.StdoutPipe()
			if err != nil {
				return fmt.Errorf("failed to create stdout pipe: %w", err)
			}
			stderr, err := workerCmd.StderrPipe()
			if err != nil {
				return fmt.Errorf("failed to create stderr pipe: %w", err)
			}

			if err := workerCmd.Start(); err != nil {
				return fmt.Errorf("failed to start %s: %w", workerBin, err)
			}

			// Stream output to terminal
			go streamLines(stdout, cmd.OutOrStdout(), "")
			go streamLines(stderr, cmd.ErrOrStderr(), "")

			if err := workerCmd.Wait(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					return fmt.Errorf("%s exited with code %d", workerBin, exitErr.ExitCode())
				}
				return fmt.Errorf("%s failed: %w", workerBin, err)
			}

			fmt.Printf("\nWorker finished. Worktree remains at %s\n", entry.Path)
			fmt.Printf("Use 'ok-gobot worktrees list' to see status.\n")
			fmt.Printf("Use 'ok-gobot worktrees cleanup' to remove merged worktrees.\n")
			return nil
		},
	}

	cmd.Flags().StringVar(&repoRoot, "repo", "", "path to git repository root (default: auto-detect from cwd)")
	cmd.Flags().StringVar(&baseDir, "base-dir", "", "base directory for new worktrees (default: ~/worktrees/<repo>)")
	cmd.Flags().StringVar(&model, "model", "", "model to pass to the worker")
	cmd.Flags().StringVar(&workerBin, "worker", "", "worker binary to use: claude (default), codex")

	return cmd
}

// newWorktreesCommand implements `ok-gobot worktrees` with subcommands.
func newWorktreesCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktrees",
		Short: "Manage auto-created git worktrees",
	}

	cmd.AddCommand(newWorktreesListCommand(cfg))
	cmd.AddCommand(newWorktreesCleanupCommand(cfg))
	cmd.AddCommand(newWorktreesRmCommand(cfg))

	return cmd
}

// newWorktreesListCommand implements `ok-gobot worktrees list`.
func newWorktreesListCommand(cfg *config.Config) *cobra.Command {
	var refresh bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tracked worktrees with status",
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := worker.DefaultWorktreeManager()
			if err != nil {
				return err
			}

			if refresh {
				fmt.Fprintln(cmd.OutOrStdout(), "Refreshing PR status...")
				if err := mgr.RefreshPRStatus(); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to refresh PR status: %v\n", err)
				}
			}

			entries, err := mgr.List()
			if err != nil {
				return fmt.Errorf("failed to list worktrees: %w", err)
			}

			if len(entries) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No tracked worktrees.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tBRANCH\tPR\tAGE\tTASK")
			for _, e := range entries {
				pr := "-"
				if e.PRNumber > 0 {
					pr = fmt.Sprintf("#%d (%s)", e.PRNumber, e.PRStatus)
				}
				age := formatAge(e.CreatedAt)
				task := truncate(e.Task, 50)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					e.ID, e.Status, e.Branch, pr, age, task)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVarP(&refresh, "refresh", "r", false, "refresh PR status from GitHub before listing")
	return cmd
}

// newWorktreesCleanupCommand implements `ok-gobot worktrees cleanup`.
func newWorktreesCleanupCommand(cfg *config.Config) *cobra.Command {
	var (
		staleDays  int
		mergedOnly bool
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Remove merged, closed, or stale worktrees",
		Long: `Check GitHub PR status and remove worktrees whose PRs have been merged or closed.
Also removes worktrees older than --stale-days (configurable via worktree.stale_age_days).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if staleDays == 0 {
				staleDays = cfg.Worktree.StaleAgeDays
				if staleDays == 0 {
					staleDays = 7
				}
			}

			mgr, err := worker.DefaultWorktreeManager()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()

			if dryRun {
				return runCleanupDryRun(mgr, out, staleDays, mergedOnly)
			}

			// Cleanup merged/closed
			deleted, err := mgr.CleanupMerged()
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: cleanup merged failed: %v\n", err)
			}
			for _, id := range deleted {
				fmt.Fprintf(out, "Removed (merged/closed): %s\n", id)
			}

			if !mergedOnly {
				// Cleanup stale
				staleAge := time.Duration(staleDays) * 24 * time.Hour
				staleDeleted, staleErr := mgr.CleanupStale(staleAge)
				if staleErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: cleanup stale failed: %v\n", staleErr)
				}
				for _, id := range staleDeleted {
					fmt.Fprintf(out, "Removed (stale >%dd): %s\n", staleDays, id)
				}
				deleted = append(deleted, staleDeleted...)
			}

			if len(deleted) == 0 {
				fmt.Fprintln(out, "Nothing to clean up.")
			} else {
				fmt.Fprintf(out, "Removed %d worktree(s).\n", len(deleted))
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&staleDays, "stale-days", 0, "remove worktrees older than N days (default: worktree.stale_age_days config, fallback 7)")
	cmd.Flags().BoolVar(&mergedOnly, "merged-only", false, "only remove merged/closed PRs, skip stale age check")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be removed without actually removing")
	return cmd
}

// newWorktreesRmCommand implements `ok-gobot worktrees rm <id>`.
func newWorktreesRmCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>",
		Short: "Remove a specific worktree by ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			mgr, err := worker.DefaultWorktreeManager()
			if err != nil {
				return err
			}

			id := args[0]
			entry, err := mgr.Get(id)
			if err != nil {
				return err
			}
			if entry == nil {
				return fmt.Errorf("worktree %q not found", id)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Removing worktree %s (branch: %s, path: %s)...\n",
				id, entry.Branch, entry.Path)

			if err := mgr.DeleteWorktree(id); err != nil {
				return fmt.Errorf("failed to remove worktree: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s.\n", id)
			return nil
		},
	}
}

// --- helpers ---

// buildWorkerArgs constructs the CLI arguments for the given worker binary.
func buildWorkerArgs(workerBin, model, task string) []string {
	switch workerBin {
	case "claude":
		args := []string{"-p"}
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, task)
		return args
	case "codex":
		args := []string{"-q"}
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, task)
		return args
	default:
		// Unknown worker: pass the task as a positional argument
		return []string{task}
	}
}

// streamLines copies lines from r to w with an optional prefix.
func streamLines(r io.Reader, w io.Writer, prefix string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if prefix != "" {
			fmt.Fprintf(w, "%s%s\n", prefix, scanner.Text())
		} else {
			fmt.Fprintln(w, scanner.Text())
		}
	}
}

// formatAge returns a human-readable age string.
func formatAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// runCleanupDryRun shows what cleanup would do without making changes.
func runCleanupDryRun(mgr *worker.WorktreeManager, out io.Writer, staleDays int, mergedOnly bool) error {
	if err := mgr.RefreshPRStatus(); err != nil {
		fmt.Fprintf(out, "Warning: failed to refresh PR status: %v\n", err)
	}

	entries, err := mgr.List()
	if err != nil {
		return err
	}

	staleAge := time.Duration(staleDays) * 24 * time.Hour
	cutoff := time.Now().Add(-staleAge)
	found := false

	for _, e := range entries {
		reason := ""
		if e.Status == worker.WorktreeStatusMerged {
			reason = "merged PR"
		} else if e.Status == worker.WorktreeStatusClosed {
			reason = "closed PR"
		} else if !mergedOnly && e.CreatedAt.Before(cutoff) {
			reason = fmt.Sprintf("stale (>%dd)", staleDays)
		}
		if reason != "" {
			fmt.Fprintf(out, "[dry-run] Would remove %s (%s): %s\n", e.ID, reason, truncate(e.Task, 50))
			found = true
		}
	}

	if !found {
		fmt.Fprintln(out, "[dry-run] Nothing to clean up.")
	}
	return nil
}
