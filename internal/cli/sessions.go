package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func newSessionsCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List and fork conversation sessions",
	}

	cmd.AddCommand(newSessionsListCommand(cfg))
	cmd.AddCommand(newSessionsForkCommand(cfg))

	return cmd
}

// --- sessions list ---

func newSessionsListCommand(cfg *config.Config) *cobra.Command {
	var (
		limit     int
		onlyForks bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List conversation sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			sessions, err := store.ListSessionsV2(limit)
			if err != nil {
				return fmt.Errorf("failed to list sessions: %w", err)
			}

			if onlyForks {
				var forks []storage.SessionV2
				for _, s := range sessions {
					if s.ForkedFrom != "" {
						forks = append(forks, s)
					}
				}
				sessions = forks
			}

			if len(sessions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION KEY\tAGENT\tMSGS\tTOKENS\tFORKED FROM\tUPDATED")
			for _, s := range sessions {
				key := truncate(s.SessionKey, 50)
				forkedFrom := ""
				if s.ForkedFrom != "" {
					forkedFrom = truncate(s.ForkedFrom, 40)
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\n",
					key, s.AgentID, s.MessageCount, s.TotalTokens, forkedFrom, formatTime(s.UpdatedAt))
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum number of sessions to show")
	cmd.Flags().BoolVar(&onlyForks, "forks", false, "show only forked sessions")
	return cmd
}

// --- sessions fork ---

func newSessionsForkCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fork <session-key>",
		Short: "Fork a session, creating an independent copy with the full conversation history",
		Long: `Fork creates an independent copy of a session.

The fork starts with the same conversation history as the source session but
diverges independently — changes to either session do not affect the other.

Use 'ok-gobot sessions list' to find available session keys.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceKey := strings.TrimSpace(args[0])
			if sourceKey == "" {
				return fmt.Errorf("session key must not be empty")
			}

			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			forkKey, err := store.ForkSessionV2(sourceKey)
			if err != nil {
				return fmt.Errorf("failed to fork session: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Session forked successfully.\n")
			fmt.Fprintf(out, "Source:  %s\n", sourceKey)
			fmt.Fprintf(out, "Fork:    %s\n", forkKey)
			return nil
		},
	}
	return cmd
}
