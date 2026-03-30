package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/babysit"
	"ok-gobot/internal/config"
)

func newBabysitCommand(cfg *config.Config) *cobra.Command {
	var (
		prNumber      int
		intervalStr   string
		timeoutStr    string
		maxIterations int
		repo          string
		workDir       string
		claudeBinary  string
		notifyChatID  string
	)

	cmd := &cobra.Command{
		Use:   "babysit",
		Short: "Watch a PR and auto-fix CI, reviews, and merge conflicts",
		Long: `babysit monitors a pull request in a loop and automatically:
  - Fixes failing CI checks using Claude
  - Addresses review comments using Claude
  - Resolves merge conflicts via rebase using Claude
  - Sends Telegram notifications on significant events

Stops when the PR is merged, the timeout is reached, or --max-iterations is hit.

Examples:
  ok-gobot babysit --pr 123
  ok-gobot babysit --pr 123 --interval 3m --timeout 2h
  ok-gobot babysit --pr 123 --repo owner/repo --max-iterations 10`,

		RunE: func(cmd *cobra.Command, args []string) error {
			if prNumber <= 0 {
				return fmt.Errorf("--pr is required and must be a positive integer")
			}

			interval, err := time.ParseDuration(intervalStr)
			if err != nil {
				return fmt.Errorf("invalid --interval %q: %w", intervalStr, err)
			}

			var timeout time.Duration
			if timeoutStr != "" {
				timeout, err = time.ParseDuration(timeoutStr)
				if err != nil {
					return fmt.Errorf("invalid --timeout %q: %w", timeoutStr, err)
				}
			}

			var chatID int64
			if notifyChatID != "" {
				chatID, err = strconv.ParseInt(strings.TrimSpace(notifyChatID), 10, 64)
				if err != nil {
					return fmt.Errorf("invalid --notify-chat %q: %w", notifyChatID, err)
				}
			}

			wcfg := babysit.Config{
				Repo:          repo,
				PR:            prNumber,
				Interval:      interval,
				Timeout:       timeout,
				MaxIterations: maxIterations,
				WorkDir:       workDir,
				ClaudeBinary:  claudeBinary,
				TelegramToken: cfg.Telegram.Token,
				NotifyChatID:  chatID,
			}

			w := babysit.New(wcfg, cmd.OutOrStdout())
			return w.Run(cmd.Context())
		},
	}

	cmd.Flags().IntVar(&prNumber, "pr", 0, "Pull request number to watch (required)")
	cmd.Flags().StringVar(&intervalStr, "interval", "5m", "How often to check the PR (e.g. 1m, 5m, 10m)")
	cmd.Flags().StringVar(&timeoutStr, "timeout", "", "Maximum total runtime (e.g. 1h, 2h30m). Empty means no limit")
	cmd.Flags().IntVar(&maxIterations, "max-iterations", 0, "Maximum number of check loops (0 = unlimited)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repo in owner/repo format (default: inferred from git remote)")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "Working directory for git and claude commands (default: current dir)")
	cmd.Flags().StringVar(&claudeBinary, "claude", "claude", "Path to the claude CLI binary")
	cmd.Flags().StringVar(&notifyChatID, "notify-chat", "", "Telegram chat ID to send notifications to")

	return cmd
}
