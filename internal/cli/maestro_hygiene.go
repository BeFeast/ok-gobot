package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/hygiene"
	"ok-gobot/internal/storage"
)

type maestroHygieneOptions struct {
	repo              string
	readyLabel        string
	hardExcludeLabels []string
	limit             int
	repoRoot          string
	sourceBranch      string
	upstreamBranch    string
	worktreeStatePath string
	stalePRDays       int
	deadWorkerMinutes int
	staleApprovalMins int
	orphanedWTDays    int
	jsonOutput        bool
}

func newMaestroHygieneCommand(cfg *config.Config) *cobra.Command {
	defaults := defaultMaestroOptions(cfg)
	opts := maestroHygieneOptions{
		repo:              defaults.repo,
		readyLabel:        defaults.readyLabel,
		hardExcludeLabels: append([]string(nil), defaults.hardExcludeLabels...),
		limit:             defaults.limit,
		sourceBranch:      "main",
		upstreamBranch:    "origin/main",
		stalePRDays:       7,
		deadWorkerMinutes: 30,
		staleApprovalMins: 15,
		orphanedWTDays:    7,
	}

	cmd := &cobra.Command{
		Use:   "hygiene",
		Short: "Report stale Maestro PR, worker, approval, checkout, and worktree state",
		Long: `Report stale Maestro operational state without mutating GitHub,
Maestro storage, or local worktrees. The report groups recommendations into
safe next actions and actions that require explicit operator approval.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := runMaestroHygiene(cmd, cfg, opts)
			if err != nil {
				return err
			}
			if opts.jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}
			renderMaestroHygieneReport(cmd.OutOrStdout(), report)
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.repo, "repo", opts.repo, "GitHub repo owner/name (default: gh infers from cwd)")
	cmd.Flags().StringVar(&opts.readyLabel, "ready-label", opts.readyLabel, "label required for default issue eligibility")
	cmd.Flags().StringSliceVar(&opts.hardExcludeLabels, "hard-exclude-label", opts.hardExcludeLabels, "hard-exclude label; repeat or comma-separate")
	cmd.Flags().IntVar(&opts.limit, "limit", opts.limit, "number of PRs/issues to inspect")
	cmd.Flags().StringVar(&opts.repoRoot, "repo-root", "", "path to local repository root (default: auto-detect)")
	cmd.Flags().StringVar(&opts.sourceBranch, "source-branch", opts.sourceBranch, "local source branch checked for origin drift")
	cmd.Flags().StringVar(&opts.upstreamBranch, "upstream-branch", opts.upstreamBranch, "upstream ref used for source checkout drift")
	cmd.Flags().StringVar(&opts.worktreeStatePath, "worktree-state", "", "path to worktree inventory JSON (default: ~/.ok-gobot/worktrees.json)")
	cmd.Flags().IntVar(&opts.stalePRDays, "stale-pr-days", opts.stalePRDays, "age in days before an open PR is considered stale")
	cmd.Flags().IntVar(&opts.deadWorkerMinutes, "dead-worker-minutes", opts.deadWorkerMinutes, "minutes without live progress before a worker is considered dead")
	cmd.Flags().IntVar(&opts.staleApprovalMins, "stale-approval-minutes", opts.staleApprovalMins, "minutes before a pending approval is considered stale")
	cmd.Flags().IntVar(&opts.orphanedWTDays, "orphaned-worktree-days", opts.orphanedWTDays, "age in days before an unlinked worktree is considered orphaned")
	cmd.Flags().BoolVar(&opts.jsonOutput, "json", false, "emit JSON instead of text")
	return cmd
}

func runMaestroHygiene(cmd *cobra.Command, cfg *config.Config, opts maestroHygieneOptions) (hygiene.Report, error) {
	var store *storage.Store
	if cfg != nil && strings.TrimSpace(cfg.StoragePath) != "" {
		opened, err := storage.New(cfg.StoragePath)
		if err != nil {
			return hygiene.Report{}, fmt.Errorf("open storage: %w", err)
		}
		defer opened.Close() //nolint:errcheck
		store = opened
	}

	collect := hygiene.CollectOptions{
		Config:            cfg,
		Store:             store,
		Repo:              opts.repo,
		RepoRoot:          opts.repoRoot,
		SourceBranch:      opts.sourceBranch,
		UpstreamBranch:    opts.upstreamBranch,
		Limit:             opts.limit,
		ReadyLabel:        opts.readyLabel,
		HardExcludeLabels: opts.hardExcludeLabels,
		WorktreeStatePath: opts.worktreeStatePath,
	}
	analyze := hygiene.Options{
		StaleOpenPRAge:      time.Duration(opts.stalePRDays) * 24 * time.Hour,
		DeadWorkerAge:       time.Duration(opts.deadWorkerMinutes) * time.Minute,
		StaleApprovalAge:    time.Duration(opts.staleApprovalMins) * time.Minute,
		OrphanedWorktreeAge: time.Duration(opts.orphanedWTDays) * 24 * time.Hour,
	}
	return hygiene.BuildReport(cmd.Context(), collect, analyze)
}

func renderMaestroHygieneReport(out io.Writer, report hygiene.Report) {
	fmt.Fprintln(out, "Maestro stale-state hygiene report")
	fmt.Fprintf(out, "Generated: %s\n", report.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintln(out, "Read-only: no GitHub, Maestro, or worktree cleanup actions were performed.")
	fmt.Fprintf(out, "Summary: %s (%d finding(s), %d safe, %d approval-required)\n",
		report.Summary.Status,
		report.Summary.TotalFindings,
		report.Summary.SafeActionCount,
		report.Summary.ApprovalRequiredCount,
	)

	renderHygieneFindings(out, "Safe next actions", report.SafeActions)
	renderHygieneFindings(out, "Approval-required actions", report.ApprovalRequired)
	renderHygieneFindings(out, "Warnings", report.Warnings)
}

func renderHygieneFindings(out io.Writer, title string, findings []hygiene.Finding) {
	fmt.Fprintf(out, "\n%s:\n", title)
	if len(findings) == 0 {
		fmt.Fprintln(out, "  none")
		return
	}
	for _, finding := range findings {
		fmt.Fprintf(out, "  - %s [%s]: %s\n", finding.ID, finding.Severity, finding.Title)
		if strings.TrimSpace(finding.Detail) != "" {
			fmt.Fprintf(out, "    Detail: %s\n", finding.Detail)
		}
		if evidence := formatHygieneEvidence(finding.Evidence); evidence != "" {
			fmt.Fprintf(out, "    Evidence: %s\n", evidence)
		}
		if strings.TrimSpace(finding.Recommendation) != "" {
			fmt.Fprintf(out, "    Recommendation: %s\n", finding.Recommendation)
		}
	}
}

func formatHygieneEvidence(e hygiene.Evidence) string {
	parts := make([]string, 0, 6)
	if e.SessionKey != "" {
		parts = append(parts, "session="+e.SessionKey)
	}
	if e.JobID != "" {
		parts = append(parts, "job="+e.JobID)
	}
	if e.IssueNumber > 0 {
		parts = append(parts, fmt.Sprintf("issue=#%d", e.IssueNumber))
	}
	if e.PRNumber > 0 {
		parts = append(parts, fmt.Sprintf("pr=#%d", e.PRNumber))
	}
	if e.Branch != "" {
		parts = append(parts, "branch="+e.Branch)
	}
	if e.WorktreePath != "" {
		parts = append(parts, "worktree="+e.WorktreePath)
	}
	if !e.StateTimestamp.IsZero() {
		parts = append(parts, "state_at="+e.StateTimestamp.Format(time.RFC3339))
	}
	return strings.Join(parts, ", ")
}
