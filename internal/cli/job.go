package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func newJobCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "Manage background jobs",
	}

	cmd.AddCommand(newJobListCommand(cfg))
	cmd.AddCommand(newJobInspectCommand(cfg))
	cmd.AddCommand(newJobCancelCommand(cfg))
	cmd.AddCommand(newJobRetryCommand(cfg))
	cmd.AddCommand(newJobTailCommand(cfg))

	return cmd
}

func newJobListCommand(cfg *config.Config) *cobra.Command {
	var (
		limit      int
		status     string
		worker     string
		kind       string
		jsonOutput bool
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

			filtered, err := store.ListJobsFiltered(storage.JobFilter{
				Status: status,
				Worker: worker,
				Kind:   kind,
				Limit:  limit,
			})
			if err != nil {
				return fmt.Errorf("failed to list jobs: %w", err)
			}

			if len(filtered) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No jobs found.")
				return nil
			}

			if jsonOutput {
				return printJobsJSON(cmd.OutOrStdout(), filtered)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%-38s  %-12s  %-14s  %-20s  %s\n",
				"JOB ID", "KIND", "STATUS", "WORKER", "CREATED")
			for _, j := range filtered {
				fmt.Fprintf(out, "%-38s  %-12s  %-14s  %-20s  %s\n",
					truncate(j.JobID, 38),
					truncate(j.Kind, 12),
					formatStatus(j),
					truncate(j.Worker, 20),
					formatTime(j.CreatedAt),
				)
			}
			return nil
		},
	}

	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "Maximum number of jobs to return")
	cmd.Flags().StringVarP(&status, "status", "s", "", "Filter by status (pending, running, succeeded, failed, cancelled, timed_out)")
	cmd.Flags().StringVarP(&worker, "worker", "w", "", "Filter by worker")
	cmd.Flags().StringVarP(&kind, "kind", "k", "", "Filter by job kind")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func newJobInspectCommand(cfg *config.Config) *cobra.Command {
	var (
		eventsLimit    int
		artifactsLimit int
		jsonOutput     bool
	)

	cmd := &cobra.Command{
		Use:   "inspect <job-id>",
		Short: "Show detailed information about a job",
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

			events, err := store.ListJobEvents(jobID, eventsLimit)
			if err != nil {
				return fmt.Errorf("failed to list events: %w", err)
			}

			artifacts, err := store.ListJobArtifacts(jobID, artifactsLimit)
			if err != nil {
				return fmt.Errorf("failed to list artifacts: %w", err)
			}

			if jsonOutput {
				return printInspectJSON(cmd.OutOrStdout(), job, events, artifacts)
			}

			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "Job:         %s\n", job.JobID)
			fmt.Fprintf(out, "Kind:        %s\n", job.Kind)
			fmt.Fprintf(out, "Status:      %s\n", formatStatus(*job))
			fmt.Fprintf(out, "Worker:      %s\n", job.Worker)
			fmt.Fprintf(out, "Description: %s\n", job.Description)
			fmt.Fprintf(out, "Attempt:     %d/%d\n", job.Attempt, job.MaxAttempts)
			if job.TimeoutSeconds > 0 {
				fmt.Fprintf(out, "Timeout:     %ds\n", job.TimeoutSeconds)
			}
			if job.RetryOfJobID != "" {
				fmt.Fprintf(out, "Retry of:    %s\n", job.RetryOfJobID)
			}
			if job.SessionKey != "" {
				fmt.Fprintf(out, "Session:     %s\n", job.SessionKey)
			}
			fmt.Fprintf(out, "Created:     %s\n", job.CreatedAt)
			if job.StartedAt != "" {
				fmt.Fprintf(out, "Started:     %s\n", job.StartedAt)
			}
			if job.CompletedAt != "" {
				fmt.Fprintf(out, "Completed:   %s\n", job.CompletedAt)
			}
			if job.Summary != "" {
				fmt.Fprintf(out, "Summary:     %s\n", job.Summary)
			}
			if job.Error != "" {
				fmt.Fprintf(out, "Error:       %s\n", job.Error)
			}

			if len(events) > 0 {
				fmt.Fprintf(out, "\nEvents (%d):\n", len(events))
				for _, e := range events {
					printEvent(out, e)
				}
			}

			if len(artifacts) > 0 {
				fmt.Fprintf(out, "\nArtifacts (%d):\n", len(artifacts))
				for _, a := range artifacts {
					fmt.Fprintf(out, "  %-20s  %-12s  %s\n", a.Name, a.ArtifactType, a.MimeType)
				}
			}

			return nil
		},
	}

	cmd.Flags().IntVar(&eventsLimit, "events", 100, "Maximum number of events to show")
	cmd.Flags().IntVar(&artifactsLimit, "artifacts", 50, "Maximum number of artifacts to show")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")

	return cmd
}

func newJobCancelCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Request cancellation of a running job",
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
			case "succeeded", "failed", "cancelled", "timed_out":
				return fmt.Errorf("job %q is already %s", jobID, job.Status)
			}

			if err := store.UpdateJobCancelRequested(jobID, true); err != nil {
				return fmt.Errorf("failed to request cancellation: %w", err)
			}

			if err := store.AddJobEvent(storage.JobEvent{
				JobID:     jobID,
				EventType: "cancel_requested",
				Message:   "cancellation requested via CLI",
			}); err != nil {
				return fmt.Errorf("failed to add cancel event: %w", err)
			}

			if job.Status == "pending" {
				cancelled, err := store.MarkJobCancelledIfPending(jobID, "cancelled via CLI before start")
				if err != nil {
					return fmt.Errorf("failed to mark job cancelled: %w", err)
				}
				if cancelled {
					if err := store.AddJobEvent(storage.JobEvent{
						JobID:     jobID,
						EventType: "cancelled",
						Message:   "cancelled via CLI before start",
					}); err != nil {
						return fmt.Errorf("failed to add cancelled event: %w", err)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Job %s cancelled (was pending).\n", jobID)
					return nil
				}
				// Job was picked up by the daemon between our read and update;
				// fall through to the "cancellation requested" path.
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Cancellation requested for job %s. The running daemon will cancel it.\n", jobID)
			return nil
		},
	}
}

func newJobRetryCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "retry <job-id>",
		Short: "Request retry of a failed or cancelled job",
		Long: `Request retry of a failed or cancelled job.

Creates a new pending job linked to the original. The running daemon
will pick it up and execute it.`,
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
				return fmt.Errorf("job %q is still %s — cannot retry", jobID, job.Status)
			case "succeeded", "timed_out":
				return fmt.Errorf("job %q is %s — cannot retry", jobID, job.Status)
			}

			if job.MaxAttempts > 0 && job.Attempt >= job.MaxAttempts {
				return fmt.Errorf("job %q reached max attempts (%d)", jobID, job.MaxAttempts)
			}

			newJob := storage.Job{
				JobID:              fmt.Sprintf("job-%d-retry", time.Now().UnixNano()),
				Kind:               job.Kind,
				Worker:             job.Worker,
				SessionKey:         job.SessionKey,
				DeliverySessionKey: job.DeliverySessionKey,
				RetryOfJobID:       job.JobID,
				Description:        job.Description,
				Status:             "pending",
				Attempt:            job.Attempt + 1,
				MaxAttempts:        job.MaxAttempts,
				TimeoutSeconds:     job.TimeoutSeconds,
			}

			if err := store.CreateJob(newJob); err != nil {
				return fmt.Errorf("failed to create retry job: %w", err)
			}

			if err := store.AddJobEvent(storage.JobEvent{
				JobID:     jobID,
				EventType: "retry_requested",
				Message:   fmt.Sprintf("retry queued as %s via CLI", newJob.JobID),
			}); err != nil {
				return fmt.Errorf("failed to add retry event: %w", err)
			}
			if err := store.AddJobEvent(storage.JobEvent{
				JobID:     newJob.JobID,
				EventType: "created",
				Message:   fmt.Sprintf("retry of %s (attempt %d)", jobID, newJob.Attempt),
			}); err != nil {
				return fmt.Errorf("failed to add created event: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Retry job %s created (attempt %d, retry of %s).\n",
				newJob.JobID, newJob.Attempt, jobID)
			return nil
		},
	}
}

func newJobTailCommand(cfg *config.Config) *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "tail <job-id>",
		Short: "Show recent events for a job",
		Long: `Show recent events for a job.

Use --follow to poll for new events until the job completes.`,
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

			var lastID int64
			if len(events) > 0 {
				lastID = events[len(events)-1].ID
			}

			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case <-time.After(1 * time.Second):
				}

				events, err = store.ListJobEvents(jobID, 1000)
				if err != nil {
					return fmt.Errorf("failed to poll events: %w", err)
				}

				for _, e := range events {
					if e.ID > lastID {
						printEvent(out, e)
						lastID = e.ID
					}
				}

				job, err = store.GetJob(jobID)
				if err != nil {
					return fmt.Errorf("failed to check job status: %w", err)
				}
				if job == nil {
					return nil
				}
				switch job.Status {
				case "succeeded", "failed", "cancelled", "timed_out":
					return nil
				}
			}
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Poll for new events until job completes")

	return cmd
}

// Helpers

func printEvent(out io.Writer, e storage.JobEvent) {
	msg := e.Message
	if msg == "" {
		msg = e.EventType
	}
	fmt.Fprintf(out, "  %s  %-18s  %s\n", formatTime(e.CreatedAt), e.EventType, msg)
}

func formatStatus(j storage.Job) string {
	if j.CancelRequested && j.Status == "running" {
		return "running (cancel)"
	}
	return j.Status
}

func formatTime(ts string) string {
	if ts == "" {
		return "-"
	}
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

func printJobsJSON(w io.Writer, jobs []storage.Job) error {
	type jsonJob struct {
		JobID       string `json:"job_id"`
		Kind        string `json:"kind"`
		Status      string `json:"status"`
		Worker      string `json:"worker"`
		Description string `json:"description"`
		Attempt     int    `json:"attempt"`
		MaxAttempts int    `json:"max_attempts"`
		CreatedAt   string `json:"created_at"`
		StartedAt   string `json:"started_at,omitempty"`
		CompletedAt string `json:"completed_at,omitempty"`
		Summary     string `json:"summary,omitempty"`
		Error       string `json:"error,omitempty"`
	}

	out := make([]jsonJob, len(jobs))
	for i, j := range jobs {
		out[i] = jsonJob{
			JobID:       j.JobID,
			Kind:        j.Kind,
			Status:      j.Status,
			Worker:      j.Worker,
			Description: j.Description,
			Attempt:     j.Attempt,
			MaxAttempts: j.MaxAttempts,
			CreatedAt:   j.CreatedAt,
			StartedAt:   j.StartedAt,
			CompletedAt: j.CompletedAt,
			Summary:     j.Summary,
			Error:       j.Error,
		}
	}

	return writeJSON(w, out)
}

func printInspectJSON(w io.Writer, job *storage.Job, events []storage.JobEvent, artifacts []storage.JobArtifact) error {
	type jsonEvent struct {
		ID        int64  `json:"id"`
		EventType string `json:"event_type"`
		Message   string `json:"message"`
		Payload   string `json:"payload,omitempty"`
		CreatedAt string `json:"created_at"`
	}
	type jsonArtifact struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		ArtifactType string `json:"artifact_type"`
		MimeType     string `json:"mime_type,omitempty"`
		URI          string `json:"uri,omitempty"`
		CreatedAt    string `json:"created_at"`
	}
	type jsonInspect struct {
		JobID              string         `json:"job_id"`
		Kind               string         `json:"kind"`
		Status             string         `json:"status"`
		CancelRequested    bool           `json:"cancel_requested"`
		Worker             string         `json:"worker"`
		SessionKey         string         `json:"session_key,omitempty"`
		DeliverySessionKey string         `json:"delivery_session_key,omitempty"`
		RetryOfJobID       string         `json:"retry_of_job_id,omitempty"`
		Description        string         `json:"description"`
		Attempt            int            `json:"attempt"`
		MaxAttempts        int            `json:"max_attempts"`
		TimeoutSeconds     int            `json:"timeout_seconds"`
		Summary            string         `json:"summary,omitempty"`
		Error              string         `json:"error,omitempty"`
		CreatedAt          string         `json:"created_at"`
		StartedAt          string         `json:"started_at,omitempty"`
		CompletedAt        string         `json:"completed_at,omitempty"`
		Events             []jsonEvent    `json:"events"`
		Artifacts          []jsonArtifact `json:"artifacts"`
	}

	evts := make([]jsonEvent, len(events))
	for i, e := range events {
		evts[i] = jsonEvent{
			ID:        e.ID,
			EventType: e.EventType,
			Message:   e.Message,
			Payload:   e.Payload,
			CreatedAt: e.CreatedAt,
		}
	}

	arts := make([]jsonArtifact, len(artifacts))
	for i, a := range artifacts {
		arts[i] = jsonArtifact{
			ID:           a.ID,
			Name:         a.Name,
			ArtifactType: a.ArtifactType,
			MimeType:     a.MimeType,
			URI:          a.URI,
			CreatedAt:    a.CreatedAt,
		}
	}

	return writeJSON(w, jsonInspect{
		JobID:              job.JobID,
		Kind:               job.Kind,
		Status:             job.Status,
		CancelRequested:    job.CancelRequested,
		Worker:             job.Worker,
		SessionKey:         job.SessionKey,
		DeliverySessionKey: job.DeliverySessionKey,
		RetryOfJobID:       job.RetryOfJobID,
		Description:        job.Description,
		Attempt:            job.Attempt,
		MaxAttempts:        job.MaxAttempts,
		TimeoutSeconds:     job.TimeoutSeconds,
		Summary:            job.Summary,
		Error:              job.Error,
		CreatedAt:          job.CreatedAt,
		StartedAt:          job.StartedAt,
		CompletedAt:        job.CompletedAt,
		Events:             evts,
		Artifacts:          arts,
	})
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
