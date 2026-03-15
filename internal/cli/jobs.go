package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/app"
	"ok-gobot/internal/storage"
)

func newJobCommand(a *app.App) *cobra.Command {
	var (
		status       string
		limit        int
		follow       bool
		pollInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "job",
		Short: "Inspect and control background jobs",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List recent jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			var jobStatus storage.JobStatus
			if strings.TrimSpace(status) != "" {
				jobStatus = storage.JobStatus(status)
			}

			jobsList, err := a.ListJobs(limit, jobStatus)
			if err != nil {
				return err
			}
			if len(jobsList) == 0 {
				fmt.Println("No jobs found.")
				return nil
			}

			for _, job := range jobsList {
				fmt.Printf("#%d  %-10s  %-12s  %-12s  %s\n",
					job.ID, job.Status, job.WorkerBackend, job.TaskType, job.Summary)
			}
			return nil
		},
	}
	listCmd.Flags().StringVar(&status, "status", "", "Filter by status")
	listCmd.Flags().IntVar(&limit, "limit", 20, "Maximum number of jobs to show")

	inspectCmd := &cobra.Command{
		Use:   "inspect <job-id>",
		Short: "Show one job with events and artifacts",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid job id: %w", err)
			}

			job, events, artifacts, err := a.GetJob(jobID)
			if err != nil {
				return err
			}
			if job == nil {
				return fmt.Errorf("job %d not found", jobID)
			}

			fmt.Printf("Job #%d\n", job.ID)
			fmt.Printf("Status: %s\n", job.Status)
			fmt.Printf("Worker: %s\n", job.WorkerBackend)
			fmt.Printf("Task: %s\n", job.Summary)
			fmt.Printf("Created: %s\n", job.CreatedAt)
			if job.StartedAt != "" {
				fmt.Printf("Started: %s\n", job.StartedAt)
			}
			if job.FinishedAt != "" {
				fmt.Printf("Finished: %s\n", job.FinishedAt)
			}
			if job.Error != "" {
				fmt.Printf("Error: %s\n", job.Error)
			}

			if job.ResultPayload != "" {
				var pretty map[string]interface{}
				if json.Unmarshal([]byte(job.ResultPayload), &pretty) == nil {
					data, _ := json.MarshalIndent(pretty, "", "  ")
					fmt.Printf("\nResult:\n%s\n", string(data))
				} else {
					fmt.Printf("\nResult:\n%s\n", job.ResultPayload)
				}
			}

			if len(events) > 0 {
				fmt.Println("\nEvents:")
				for _, event := range events {
					fmt.Printf("- [%s] %s: %s\n", event.CreatedAt, event.EventType, event.Message)
				}
			}
			if len(artifacts) > 0 {
				fmt.Println("\nArtifacts:")
				for _, artifact := range artifacts {
					fmt.Printf("- %s (%s): %s\n", artifact.Name, artifact.Kind, artifact.URI)
				}
			}
			return nil
		},
	}

	tailCmd := &cobra.Command{
		Use:   "tail <job-id>",
		Short: "Stream new lifecycle events until the job reaches a terminal state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid job id: %w", err)
			}
			return tailJob(cmd.Context(), a, jobID, follow, pollInterval)
		},
	}
	tailCmd.Flags().BoolVar(&follow, "follow", true, "Keep polling until the job reaches a terminal state")
	tailCmd.Flags().DurationVar(&pollInterval, "poll", 2*time.Second, "Polling interval while following")

	cancelCmd := &cobra.Command{
		Use:   "cancel <job-id>",
		Short: "Cancel a queued or running job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid job id: %w", err)
			}
			if err := a.CancelJob(jobID); err != nil {
				return err
			}
			fmt.Printf("Cancelled job #%d\n", jobID)
			return nil
		},
	}

	retryCmd := &cobra.Command{
		Use:   "retry <job-id>",
		Short: "Retry a completed or failed job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			jobID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid job id: %w", err)
			}
			job, err := a.RetryJob(context.Background(), jobID)
			if err != nil {
				return err
			}
			fmt.Printf("Retried job #%d as new job #%d\n", jobID, job.ID)
			return nil
		},
	}

	cmd.AddCommand(listCmd, inspectCmd, tailCmd, cancelCmd, retryCmd)
	return cmd
}

func newWorkerCommand(a *app.App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "List configured worker backends",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List worker backends",
		Run: func(cmd *cobra.Command, args []string) {
			workers := a.ListWorkers()
			if len(workers) == 0 {
				fmt.Println("No workers configured.")
				return
			}
			for _, worker := range workers {
				defaultMark := ""
				if worker.Default {
					defaultMark = " (default)"
				}
				fmt.Printf("%s%s\n  binary: %s\n  %s\n", worker.Name, defaultMark, worker.Binary, worker.Description)
			}
		},
	}

	cmd.AddCommand(listCmd)
	return cmd
}

func tailJob(ctx context.Context, a *app.App, jobID int64, follow bool, pollInterval time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}

	var (
		headerPrinted bool
		lastEventID   int64
		finalPrinted  bool
	)

	for {
		job, events, artifacts, err := a.GetJob(jobID)
		if err != nil {
			return err
		}
		if job == nil {
			return fmt.Errorf("job %d not found", jobID)
		}

		if !headerPrinted {
			fmt.Printf("Job #%d  %s  %s  %s\n", job.ID, job.Status, job.WorkerBackend, job.Summary)
			headerPrinted = true
		}

		for _, event := range events {
			if event.ID <= lastEventID {
				continue
			}
			fmt.Printf("[%s] %s: %s\n", event.CreatedAt, event.EventType, event.Message)
			lastEventID = event.ID
		}

		if isTerminalJobStatus(job.Status) && !finalPrinted {
			finalPrinted = true
			fmt.Printf("Final status: %s\n", job.Status)
			if job.Error != "" {
				fmt.Printf("Error: %s\n", job.Error)
			}
			if job.ResultPayload != "" {
				var pretty map[string]interface{}
				if json.Unmarshal([]byte(job.ResultPayload), &pretty) == nil {
					data, _ := json.MarshalIndent(pretty, "", "  ")
					fmt.Printf("Result:\n%s\n", string(data))
				} else {
					fmt.Printf("Result:\n%s\n", job.ResultPayload)
				}
			}
			if len(artifacts) > 0 {
				fmt.Println("Artifacts:")
				for _, artifact := range artifacts {
					fmt.Printf("- %s (%s): %s\n", artifact.Name, artifact.Kind, artifact.URI)
				}
			}
		}

		if !follow || isTerminalJobStatus(job.Status) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func isTerminalJobStatus(status storage.JobStatus) bool {
	switch status {
	case storage.JobDone, storage.JobFailed, storage.JobCancelled:
		return true
	default:
		return false
	}
}
