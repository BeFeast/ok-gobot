package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

// newEvolutionCommand returns the `evolution` top-level CLI command.
func newEvolutionCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evolution",
		Short: "Inspect and manage the agent self-evolution loop",
	}

	cmd.AddCommand(newEvolutionStatusCommand(cfg))
	cmd.AddCommand(newEvolutionHistoryCommand(cfg))
	cmd.AddCommand(newEvolutionRollbackCommand(cfg))
	cmd.AddCommand(newEvolutionMetricsCommand(cfg))

	return cmd
}

// --- status ---

func newEvolutionStatusCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show current evolution configuration and latest version",
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "Evolution loop:   %v\n", cfg.Evolution.Enabled)
			fmt.Fprintf(out, "Tasks per cycle:  %d\n", cfg.Evolution.TasksPerCycle)
			fmt.Fprintf(out, "Pass threshold:   %.0f%%\n", cfg.Evolution.PassThreshold*100)
			fmt.Fprintf(out, "Max diff:         %.0f%%\n", cfg.Evolution.MaxDiffPercent*100)
			fmt.Fprintf(out, "Benchmarks dir:   %s\n", cfg.Evolution.BenchmarksDir)
			fmt.Fprintf(out, "Evolution dir:    %s\n", cfg.Evolution.EvolutionDir)

			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			latest, err := store.GetLatestEvolutionVersion()
			if err != nil {
				return fmt.Errorf("get latest version: %w", err)
			}

			fmt.Fprintln(out)
			if latest == nil {
				fmt.Fprintln(out, "No evolution cycles completed yet.")
				return nil
			}

			fmt.Fprintf(out, "Current version:  v%d\n", latest.Version)
			fmt.Fprintf(out, "Benchmark score:  %.2f\n", latest.BenchmarkScore)
			fmt.Fprintf(out, "Promoted at:      %s\n", latest.PromotedAt)
			if latest.RolledBackAt != "" {
				fmt.Fprintf(out, "Rolled back at:   %s\n", latest.RolledBackAt)
			}
			if latest.Notes != "" {
				fmt.Fprintf(out, "Notes:\n%s\n", latest.Notes)
			}

			// Summary of recent metrics.
			summary, err := store.SummarizeRecentMetrics(100)
			if err != nil {
				return fmt.Errorf("get metrics summary: %w", err)
			}
			fmt.Fprintln(out)
			fmt.Fprintf(out, "Recent tasks:     %d\n", summary.Total)
			if summary.Total > 0 {
				fmt.Fprintf(out, "Success rate:     %.1f%%\n", summary.SuccessRate*100)
				fmt.Fprintf(out, "Avg tokens:       %.0f\n", summary.AvgTokens)
				fmt.Fprintf(out, "Avg duration:     %.0fs\n", summary.AvgDurationMS/1000)
			}
			return nil
		},
	}
}

// --- history ---

func newEvolutionHistoryCommand(cfg *config.Config) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List all evolution version records",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			versions, err := store.ListEvolutionVersions(limit)
			if err != nil {
				return fmt.Errorf("list versions: %w", err)
			}

			if len(versions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No evolution history found.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "VERSION\tSCORE\tHASH\tPROMOTED\tROLLED BACK")
			for _, v := range versions {
				rolledBack := "-"
				if v.RolledBackAt != "" {
					rolledBack = formatTime(v.RolledBackAt)
				}
				fmt.Fprintf(w, "v%d\t%.2f\t%s\t%s\t%s\n",
					v.Version,
					v.BenchmarkScore,
					v.ConfigHash,
					formatTime(v.PromotedAt),
					rolledBack,
				)
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "maximum number of versions to show")
	return cmd
}

// --- rollback ---

func newEvolutionRollbackCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "rollback <version>",
		Short: "Mark an evolution version as rolled back",
		Long:  `Mark an evolution version as rolled back. The next evolution cycle will not consider it.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			var version int
			if _, err := fmt.Sscanf(args[0], "v%d", &version); err != nil {
				if _, err := fmt.Sscanf(args[0], "%d", &version); err != nil {
					return fmt.Errorf("invalid version %q — use v<N> or <N>", args[0])
				}
			}

			v, err := store.GetEvolutionVersion(version)
			if err != nil {
				return fmt.Errorf("get version: %w", err)
			}
			if v == nil {
				return fmt.Errorf("version v%d not found", version)
			}
			if v.RolledBackAt != "" {
				return fmt.Errorf("version v%d is already marked as rolled back at %s", version, v.RolledBackAt)
			}

			if err := store.MarkVersionRolledBack(version); err != nil {
				return fmt.Errorf("mark rolled back: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Version v%d marked as rolled back.\n", version)
			return nil
		},
	}
}

// --- metrics ---

func newEvolutionMetricsCommand(cfg *config.Config) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Show recent task execution metrics",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			summary, err := store.SummarizeRecentMetrics(limit)
			if err != nil {
				return fmt.Errorf("summarize metrics: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Tasks analyzed:   %d\n", summary.Total)
			if summary.Total == 0 {
				fmt.Fprintln(out, "No task metrics recorded yet.")
				return nil
			}
			fmt.Fprintf(out, "Successes:        %d\n", summary.Successes)
			fmt.Fprintf(out, "Failures:         %d\n", summary.Failures)
			fmt.Fprintf(out, "Success rate:     %.1f%%\n", summary.SuccessRate*100)
			fmt.Fprintf(out, "Avg tokens:       %.0f\n", summary.AvgTokens)
			fmt.Fprintf(out, "Avg duration:     %.0fs\n", summary.AvgDurationMS/1000)

			if len(summary.TopToolCalls) > 0 {
				fmt.Fprintln(out, "\nTop tool calls:")
				w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				for tool, count := range summary.TopToolCalls {
					fmt.Fprintf(w, "  %s\t%d\n", tool, count)
				}
				w.Flush() //nolint:errcheck
			}

			metrics, err := store.GetRecentTaskMetrics(limit)
			if err != nil {
				return fmt.Errorf("get recent metrics: %w", err)
			}

			if len(metrics) > 0 {
				fmt.Fprintln(out, "\nRecent tasks:")
				w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "  TASK ID\tSUCCESS\tTOKENS\tDURATION\tRETRIES\tCREATED")
				for _, m := range metrics {
					success := "no"
					if m.Success {
						success = "yes"
					}
					fmt.Fprintf(w, "  %s\t%s\t%d\t%.0fs\t%d\t%s\n",
						truncate(m.TaskID, 20),
						success,
						m.Tokens,
						float64(m.DurationMS)/1000,
						m.Retries,
						formatTime(m.CreatedAt),
					)
				}
				w.Flush() //nolint:errcheck
			}

			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "number of recent tasks to analyze")
	return cmd
}
