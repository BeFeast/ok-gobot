package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func newWorkerCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Inspect background workers",
	}

	cmd.AddCommand(newWorkerListCommand(cfg))
	cmd.AddCommand(newWorkerInspectCommand(cfg))

	return cmd
}

func newWorkerListCommand(cfg *config.Config) *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List workers that have run jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			jobs, err := store.ListJobsFiltered(storage.JobFilter{})
			if err != nil {
				return fmt.Errorf("failed to list jobs: %w", err)
			}

			type workerInfo struct {
				Name     string
				Total    int
				Running  int
				Pending  int
				Failed   int
				LastSeen string
			}

			seen := make(map[string]*workerInfo)
			var order []string
			for _, j := range jobs {
				name := j.Worker
				if name == "" {
					name = "(none)"
				}
				wi, ok := seen[name]
				if !ok {
					wi = &workerInfo{Name: name}
					seen[name] = wi
					order = append(order, name)
				}
				wi.Total++
				switch j.Status {
				case "running":
					wi.Running++
				case "pending":
					wi.Pending++
				case "failed":
					wi.Failed++
				}
				if wi.LastSeen == "" || j.CreatedAt > wi.LastSeen {
					wi.LastSeen = j.CreatedAt
				}
			}

			if len(order) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No workers found.")
				return nil
			}

			if jsonOutput {
				type jsonWorker struct {
					Name     string `json:"name"`
					Total    int    `json:"total"`
					Running  int    `json:"running"`
					Pending  int    `json:"pending"`
					Failed   int    `json:"failed"`
					LastSeen string `json:"last_seen"`
				}
				out := make([]jsonWorker, len(order))
				for i, name := range order {
					wi := seen[name]
					out[i] = jsonWorker{
						Name:     wi.Name,
						Total:    wi.Total,
						Running:  wi.Running,
						Pending:  wi.Pending,
						Failed:   wi.Failed,
						LastSeen: wi.LastSeen,
					}
				}
				return writeJSON(cmd.OutOrStdout(), out)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%-30s  %6s  %7s  %7s  %6s  %s\n",
				"WORKER", "TOTAL", "RUNNING", "PENDING", "FAILED", "LAST SEEN")
			for _, name := range order {
				wi := seen[name]
				fmt.Fprintf(out, "%-30s  %6d  %7d  %7d  %6d  %s\n",
					truncate(wi.Name, 30),
					wi.Total,
					wi.Running,
					wi.Pending,
					wi.Failed,
					formatTime(wi.LastSeen),
				)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func newWorkerInspectCommand(cfg *config.Config) *cobra.Command {
	var (
		limit      int
		status     string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "inspect <worker-name>",
		Short: "Show jobs for a specific worker",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			workerName := args[0]
			filtered, err := store.ListJobsFiltered(storage.JobFilter{
				Worker: workerName,
				Status: status,
				Limit:  limit,
			})
			if err != nil {
				return fmt.Errorf("failed to list jobs: %w", err)
			}

			if len(filtered) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No jobs found for worker %q.\n", workerName)
				return nil
			}

			if jsonOutput {
				return printJobsJSON(cmd.OutOrStdout(), filtered)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Jobs for worker %q:\n\n", workerName)
			fmt.Fprintf(out, "%-38s  %-12s  %-14s  %s\n",
				"JOB ID", "KIND", "STATUS", "CREATED")
			for _, j := range filtered {
				fmt.Fprintf(out, "%-38s  %-12s  %-14s  %s\n",
					truncate(j.JobID, 38),
					truncate(j.Kind, 12),
					formatStatus(j),
					formatTime(j.CreatedAt),
				)
			}
			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum number of jobs to return")
	cmd.Flags().StringVarP(&status, "status", "s", "", "Filter by status")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}
