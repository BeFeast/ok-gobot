package cli

import (
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/maestro"
	"ok-gobot/internal/prhygiene"
)

type maestroOptions struct {
	repo              string
	readyLabel        string
	hardExcludeLabels []string
	limit             int
	override          bool
	overrideReason    string
}

type maestroStatus struct {
	Decision       maestro.Decision
	PRBlockers     []prhygiene.Blocker
	PRBlockerError string
	Now            time.Time
}

func newMaestroCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "maestro",
		Short: "Inspect strict GitHub issue intake for worker selection",
	}
	cmd.AddCommand(newMaestroDryRunCommand(cfg))
	cmd.AddCommand(newMaestroStatusCommand(cfg))
	cmd.AddCommand(newMaestroHygieneCommand(cfg))
	return cmd
}

func newMaestroDryRunCommand(cfg *config.Config) *cobra.Command {
	opts := defaultMaestroOptions(cfg)
	cmd := &cobra.Command{
		Use:   "dry-run",
		Short: "Show the next eligible issue and skipped candidates",
		RunE: func(cmd *cobra.Command, args []string) error {
			decision, err := runMaestroDryRun(cmd, opts)
			if err != nil {
				return err
			}
			renderMaestroDecision(cmd.OutOrStdout(), decision, "dry-run")
			return nil
		},
	}
	bindMaestroFlags(cmd, &opts)
	return cmd
}

func newMaestroStatusCommand(cfg *config.Config) *cobra.Command {
	opts := defaultMaestroOptions(cfg)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Explain why no Maestro worker is running",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := runMaestroStatus(cmd, opts)
			if err != nil {
				return err
			}
			renderMaestroStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
	bindMaestroFlags(cmd, &opts)
	return cmd
}

func defaultMaestroOptions(cfg *config.Config) maestroOptions {
	opts := maestroOptions{
		readyLabel:        maestro.DefaultReadyLabel,
		hardExcludeLabels: maestro.DefaultHardExcludeLabels(),
		limit:             maestro.DefaultLimit,
	}
	if cfg == nil {
		return opts
	}
	if cfg.Maestro.Repo != "" {
		opts.repo = cfg.Maestro.Repo
	}
	if cfg.Maestro.ReadyLabel != "" {
		opts.readyLabel = cfg.Maestro.ReadyLabel
	}
	if len(cfg.Maestro.HardExcludeLabels) > 0 {
		opts.hardExcludeLabels = append([]string(nil), cfg.Maestro.HardExcludeLabels...)
	}
	if cfg.Maestro.Limit > 0 {
		opts.limit = cfg.Maestro.Limit
	}
	return opts
}

func bindMaestroFlags(cmd *cobra.Command, opts *maestroOptions) {
	cmd.Flags().StringVar(&opts.repo, "repo", opts.repo, "GitHub repo owner/name (default: gh infers from cwd)")
	cmd.Flags().StringVar(&opts.readyLabel, "ready-label", opts.readyLabel, "label required for default issue eligibility")
	cmd.Flags().StringSliceVar(&opts.hardExcludeLabels, "hard-exclude-label", opts.hardExcludeLabels, "hard-exclude label; repeat or comma-separate")
	cmd.Flags().IntVar(&opts.limit, "limit", opts.limit, "number of open issues and PRs to inspect")
	cmd.Flags().BoolVar(&opts.override, "override", false, "maintainer override: select the first open issue despite intake gates")
	cmd.Flags().StringVar(&opts.overrideReason, "override-reason", "", "visible reason for maintainer override")
}

func runMaestroDryRun(cmd *cobra.Command, opts maestroOptions) (maestro.Decision, error) {
	policy := maestroPolicy(opts)
	if opts.override {
		reason := strings.TrimSpace(opts.overrideReason)
		if reason == "" {
			reason = "no reason supplied"
		}
		log.Printf("[maestro] maintainer override enabled for intake dry-run: %s", reason)
	}
	return maestro.DryRun(cmd.Context(), maestro.NewGHClient(""), policy)
}

func runMaestroStatus(cmd *cobra.Command, opts maestroOptions) (maestroStatus, error) {
	policy := maestroPolicy(opts)
	client := maestro.NewGHClient("")
	decision, err := maestro.DryRun(cmd.Context(), client, policy)
	if err != nil {
		return maestroStatus{}, err
	}
	now := time.Now()
	blockers, blockerErr := client.ListOpenPullRequestBlockers(cmd.Context(), policy.Repo, policy.Limit, prhygiene.Options{
		Now:        now,
		StaleAfter: prhygiene.DefaultStaleAfter,
	})
	status := maestroStatus{Decision: decision, PRBlockers: blockers, Now: now}
	if blockerErr != nil {
		status.PRBlockerError = blockerErr.Error()
	}
	return status, nil
}

func maestroPolicy(opts maestroOptions) maestro.Policy {
	return maestro.Policy{
		Repo:              opts.repo,
		ReadyLabel:        opts.readyLabel,
		HardExcludeLabels: opts.hardExcludeLabels,
		Limit:             opts.limit,
		Override:          opts.override,
		OverrideReason:    opts.overrideReason,
	}
}

func renderMaestroStatus(out io.Writer, status maestroStatus) {
	renderMaestroDecision(out, status.Decision, "status")
	renderPRBlockers(out, status.PRBlockers, status.PRBlockerError, status.Now)
}

func renderMaestroDecision(out io.Writer, decision maestro.Decision, mode string) {
	policy := maestro.NormalizePolicy(decision.Policy)
	fmt.Fprintf(out, "Maestro intake %s\n", mode)
	fmt.Fprintf(out, "Ready label: %q\n", policy.ReadyLabel)
	fmt.Fprintf(out, "Hard excludes: %s\n", strings.Join(policy.HardExcludeLabels, ", "))
	if strings.TrimSpace(policy.Repo) != "" {
		fmt.Fprintf(out, "Repo: %s\n", policy.Repo)
	}
	if policy.Override {
		reason := strings.TrimSpace(policy.OverrideReason)
		if reason == "" {
			reason = "no reason supplied"
		}
		fmt.Fprintf(out, "Override: ENABLED (maintainer override: %s)\n", reason)
	} else {
		fmt.Fprintln(out, "Override: disabled")
	}

	if decision.Next == nil {
		fmt.Fprintln(out, "No worker running: no eligible issue after strict intake policy.")
	} else {
		fmt.Fprintf(out, "No worker running: next eligible issue is #%d %s.\n", decision.Next.Issue.Number, decision.Next.Issue.Title)
		if decision.Next.OverrideUsed {
			fmt.Fprintln(out, "Selected by maintainer override.")
			if len(decision.Next.OverrideReasons) > 0 {
				fmt.Fprintf(out, "Override bypassed: %s\n", strings.Join(decision.Next.OverrideReasons, "; "))
			}
		}
	}

	if len(decision.Skipped) == 0 {
		fmt.Fprintln(out, "Skipped candidates: none")
		return
	}
	fmt.Fprintln(out, "Skipped candidates:")
	for _, skipped := range decision.Skipped {
		reasons := strings.Join(skipped.SkipReasons, "; ")
		if reasons == "" {
			reasons = "unknown"
		}
		fmt.Fprintf(out, "  #%d %s - %s\n", skipped.Issue.Number, skipped.Issue.Title, reasons)
	}
}

func renderPRBlockers(out io.Writer, blockers []prhygiene.Blocker, blockerErr string, now time.Time) {
	if strings.TrimSpace(blockerErr) != "" {
		fmt.Fprintf(out, "Pull request blockers: unavailable (%s)\n", blockerErr)
		return
	}
	if len(blockers) == 0 {
		fmt.Fprintln(out, "Pull request blockers: none")
		return
	}
	fmt.Fprintln(out, "Pull request blockers:")
	for _, blocker := range blockers {
		fmt.Fprintf(out, "  %s\n", formatPRBlocker(blocker, now))
	}
}

func formatPRBlocker(blocker prhygiene.Blocker, now time.Time) string {
	updated := "unknown"
	age := "unknown"
	if !blocker.UpdatedAt.IsZero() {
		updated = blocker.UpdatedAt.UTC().Format(time.RFC3339)
		age = prhygiene.FormatAge(now, blocker.UpdatedAt)
	}
	state := strings.TrimSpace(blocker.State)
	if state == "" {
		state = "OPEN"
	}
	return fmt.Sprintf("#%d %s updated %s (%s ago) - %s", blocker.Number, state, updated, age, blocker.Reason)
}
