package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func newJobsCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jobs",
		Short: "List, inspect, cancel, retry, and tail background jobs",
	}

	cmd.AddCommand(newJobsListCommand(cfg))
	cmd.AddCommand(newJobsInspectCommand(cfg))
	cmd.AddCommand(newJobsCancelCommand(cfg))
	cmd.AddCommand(newJobsRetryCommand(cfg))
	cmd.AddCommand(newJobsTailCommand(cfg))

	return cmd
}

// --- list ---

func newJobsListCommand(cfg *config.Config) *cobra.Command {
	var (
		status string
		limit  int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List background jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			var jobs []storage.Job
			if status != "" {
				jobs, err = store.ListJobsByStatus(status, limit)
			} else {
				jobs, err = store.ListJobs(limit)
			}
			if err != nil {
				return fmt.Errorf("failed to list jobs: %w", err)
			}

			if len(jobs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No jobs found.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tSTATUS\tKIND\tWORKER\tDESCRIPTION\tCREATED")
			for _, j := range jobs {
				desc := truncate(j.Description, 40)
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					j.JobID, j.Status, j.Kind, j.Worker, desc, formatTime(j.CreatedAt))
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&status, "status", "", "filter by status (pending, running, succeeded, failed, cancelled, timed_out)")
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum number of jobs to show")
	return cmd
}

// --- inspect ---

func newJobsInspectCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <job-id>",
		Short: "Show detailed information about a job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			job, err := store.GetJob(args[0])
			if err != nil {
				return fmt.Errorf("failed to get job: %w", err)
			}
			if job == nil {
				return fmt.Errorf("job %q not found", args[0])
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Job:          %s\n", job.JobID)
			fmt.Fprintf(out, "Status:       %s\n", job.Status)
			fmt.Fprintf(out, "Kind:         %s\n", job.Kind)
			if job.Worker != "" {
				fmt.Fprintf(out, "Worker:       %s\n", job.Worker)
			}
			if job.Description != "" {
				fmt.Fprintf(out, "Description:  %s\n", job.Description)
			}
			fmt.Fprintf(out, "Attempt:      %d / %d\n", job.Attempt, job.MaxAttempts)
			if job.TimeoutSeconds > 0 {
				fmt.Fprintf(out, "Timeout:      %s\n", (time.Duration(job.TimeoutSeconds) * time.Second).String())
			}
			if job.SessionKey != "" {
				fmt.Fprintf(out, "Session:      %s\n", job.SessionKey)
			}
			if job.DeliverySessionKey != "" {
				fmt.Fprintf(out, "Delivery:     %s\n", job.DeliverySessionKey)
			}
			if job.RetryOfJobID != "" {
				fmt.Fprintf(out, "Retry of:     %s\n", job.RetryOfJobID)
			}
			if job.CancelRequested {
				fmt.Fprintf(out, "Cancel req:   yes\n")
			}
			fmt.Fprintf(out, "Created:      %s\n", job.CreatedAt)
			if job.StartedAt != "" {
				fmt.Fprintf(out, "Started:      %s\n", job.StartedAt)
			}
			if job.CompletedAt != "" {
				fmt.Fprintf(out, "Completed:    %s\n", job.CompletedAt)
			}
			if job.Summary != "" {
				fmt.Fprintf(out, "Summary:      %s\n", job.Summary)
			}
			if job.Error != "" {
				fmt.Fprintf(out, "Error:        %s\n", job.Error)
			}

			// Events
			events, err := store.ListJobEvents(job.JobID, 100)
			if err != nil {
				return fmt.Errorf("failed to list events: %w", err)
			}
			if len(events) > 0 {
				fmt.Fprintln(out)
				fmt.Fprintln(out, "Events:")
				w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "  TIME\tTYPE\tMESSAGE")
				for _, e := range events {
					msg := truncate(e.Message, 60)
					fmt.Fprintf(w, "  %s\t%s\t%s\n", formatTime(e.CreatedAt), e.EventType, msg)
				}
				w.Flush() //nolint:errcheck
			}

			// Artifacts
			artifacts, err := store.ListJobArtifacts(job.JobID, 100)
			if err != nil {
				return fmt.Errorf("failed to list artifacts: %w", err)
			}
			if len(artifacts) > 0 {
				fmt.Fprintln(out)
				fmt.Fprintln(out, "Artifacts:")
				w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "  NAME\tTYPE\tMIME\tURI")
				for _, a := range artifacts {
					fmt.Fprintf(w, "  %s\t%s\t%s\t%s\n", a.Name, a.ArtifactType, a.MimeType, a.URI)
				}
				w.Flush() //nolint:errcheck
			}

			return nil
		},
	}
}

// --- cancel ---

func newJobsCancelCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Cancel a pending or running job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			jobID := args[0]
			job, err := store.GetJob(jobID)
			if err != nil {
				return fmt.Errorf("failed to get job: %w", err)
			}
			if job == nil {
				return fmt.Errorf("job %q not found", jobID)
			}

			switch job.Status {
			case "succeeded", "cancelled", "timed_out":
				return fmt.Errorf("job %q already in terminal state: %s", jobID, job.Status)
			}

			if err := store.UpdateJobCancelRequested(jobID, true); err != nil {
				return fmt.Errorf("failed to request cancellation: %w", err)
			}
			if err := store.AddJobEvent(storage.JobEvent{
				JobID:     jobID,
				EventType: "cancel_requested",
				Message:   "cancel requested via CLI",
			}); err != nil {
				return fmt.Errorf("failed to record cancel event: %w", err)
			}

			if job.Status == "pending" {
				if err := store.MarkJobCancelled(jobID, "cancelled via CLI"); err != nil {
					return fmt.Errorf("failed to mark job cancelled: %w", err)
				}
				if err := store.AddJobEvent(storage.JobEvent{
					JobID:     jobID,
					EventType: "cancelled",
					Message:   "cancelled via CLI",
				}); err != nil {
					return fmt.Errorf("failed to record cancelled event: %w", err)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Job %s cancelled.\n", jobID)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Cancellation requested for job %s (currently %s).\n", jobID, job.Status)
			}
			return nil
		},
	}
}

// --- retry ---

func newJobsRetryCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "retry <job-id>",
		Short: "Queue a retry for a completed job",
		Long: `Queue a retry for a completed (failed, cancelled, timed_out, or succeeded) job.

Creates a new pending job record linked to the original. The retry will be
picked up by the running bot instance.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			jobID := args[0]
			job, err := store.GetJob(jobID)
			if err != nil {
				return fmt.Errorf("failed to get job: %w", err)
			}
			if job == nil {
				return fmt.Errorf("job %q not found", jobID)
			}

			switch job.Status {
			case "pending", "running":
				return fmt.Errorf("job %q is still %s and cannot be retried", jobID, job.Status)
			}

			if job.MaxAttempts > 0 && job.Attempt >= job.MaxAttempts {
				return fmt.Errorf("job %q reached max attempts (%d)", jobID, job.MaxAttempts)
			}

			newID := newCLIJobID()
			attempt := job.Attempt + 1
			if err := store.CreateJob(storage.Job{
				JobID:              newID,
				Kind:               job.Kind,
				Worker:             job.Worker,
				SessionKey:         job.SessionKey,
				DeliverySessionKey: job.DeliverySessionKey,
				RetryOfJobID:       job.JobID,
				Description:        job.Description,
				Status:             "pending",
				Attempt:            attempt,
				MaxAttempts:        job.MaxAttempts,
				TimeoutSeconds:     job.TimeoutSeconds,
			}); err != nil {
				return fmt.Errorf("failed to create retry job: %w", err)
			}

			if err := store.AddJobEvent(storage.JobEvent{
				JobID:     newID,
				EventType: "created",
				Message:   fmt.Sprintf("retry of %s (attempt %d)", job.JobID, attempt),
			}); err != nil {
				return fmt.Errorf("failed to record creation event: %w", err)
			}

			if err := store.AddJobEvent(storage.JobEvent{
				JobID:     job.JobID,
				EventType: "retry_requested",
				Message:   fmt.Sprintf("retry queued as %s via CLI", newID),
			}); err != nil {
				return fmt.Errorf("failed to record retry event: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Retry queued: %s (attempt %d of %d)\n", newID, attempt, job.MaxAttempts)
			return nil
		},
	}
}

// --- tail ---

func newJobsTailCommand(cfg *config.Config) *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "tail <job-id>",
		Short: "Show recent job events",
		Long:  `Show recent lifecycle events for a job. Use --follow to poll for new events.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			jobID := args[0]
			job, err := store.GetJob(jobID)
			if err != nil {
				return fmt.Errorf("failed to get job: %w", err)
			}
			if job == nil {
				return fmt.Errorf("job %q not found", jobID)
			}

			events, err := store.ListJobEvents(jobID, 100)
			if err != nil {
				return fmt.Errorf("failed to list events: %w", err)
			}

			out := cmd.OutOrStdout()
			for _, e := range events {
				printEvent(out, e)
			}

			if !follow {
				return nil
			}

			// Poll for new events until the job reaches a terminal state or context is cancelled.
			var lastID int64
			if len(events) > 0 {
				lastID = events[len(events)-1].ID
			}

			ctx := cmd.Context()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(1 * time.Second):
				}

				newEvents, err := store.ListJobEvents(jobID, 100)
				if err != nil {
					return fmt.Errorf("failed to poll events: %w", err)
				}

				for _, e := range newEvents {
					if e.ID <= lastID {
						continue
					}
					printEvent(out, e)
					lastID = e.ID
				}

				// Check if the job has reached a terminal state.
				j, err := store.GetJob(jobID)
				if err != nil {
					return fmt.Errorf("failed to check job status: %w", err)
				}
				if j != nil && isTerminalStatus(j.Status) {
					return nil
				}
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "poll for new events until the job completes")
	return cmd
}

// --- helpers ---

func printEvent(out interface{ Write([]byte) (int, error) }, e storage.JobEvent) {
	msg := e.Message
	if msg == "" {
		msg = "-"
	}
	fmt.Fprintf(out, "%s  %-20s  %s\n", formatTime(e.CreatedAt), e.EventType, msg)
}

func isTerminalStatus(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "timed_out":
		return true
	}
	return false
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > maxLen {
		return s[:maxLen-3] + "..."
	}
	return s
}

func formatTime(ts string) string {
	// Try parsing common SQLite datetime formats, fall back to raw string.
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.Format("2006-01-02 15:04:05")
		}
	}
	return ts
}

func newCLIJobID() string {
	return fmt.Sprintf("job-%d", time.Now().UnixNano())
}
