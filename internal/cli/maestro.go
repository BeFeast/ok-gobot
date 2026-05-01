package cli

import (
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/maestro"
)

type maestroOptions struct {
	repo              string
	readyLabel        string
	hardExcludeLabels []string
	limit             int
	override          bool
	overrideReason    string
}

func newMaestroCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "maestro",
		Short: "Inspect strict GitHub issue intake for worker selection",
	}
	cmd.AddCommand(newMaestroDryRunCommand(cfg))
	cmd.AddCommand(newMaestroStatusCommand(cfg))
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
			decision, err := runMaestroDryRun(cmd, opts)
			if err != nil {
				return err
			}
			renderMaestroDecision(cmd.OutOrStdout(), decision, "status")
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
	cmd.Flags().IntVar(&opts.limit, "limit", opts.limit, "number of open issues to inspect")
	cmd.Flags().BoolVar(&opts.override, "override", false, "maintainer override: select the first open issue despite intake gates")
	cmd.Flags().StringVar(&opts.overrideReason, "override-reason", "", "visible reason for maintainer override")
}

func runMaestroDryRun(cmd *cobra.Command, opts maestroOptions) (maestro.Decision, error) {
	policy := maestro.Policy{
		Repo:              opts.repo,
		ReadyLabel:        opts.readyLabel,
		HardExcludeLabels: opts.hardExcludeLabels,
		Limit:             opts.limit,
		Override:          opts.override,
		OverrideReason:    opts.overrideReason,
	}
	if opts.override {
		reason := strings.TrimSpace(opts.overrideReason)
		if reason == "" {
			reason = "no reason supplied"
		}
		log.Printf("[maestro] maintainer override enabled for intake dry-run: %s", reason)
	}
	return maestro.DryRun(cmd.Context(), maestro.NewGHClient(""), policy)
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
