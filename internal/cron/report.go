package cron

import (
	"fmt"
	"strings"
	"time"

	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

// JobReport is the standardized report produced after every cron-triggered job.
type JobReport struct {
	CronJobID int64
	JobID     string // durable runtime job ID (empty for legacy path)
	Type      string // "llm" or "exec"
	Task      string
	Status    string // "succeeded", "failed", "timed_out"
	Summary   string
	Output    string
	Error     string
	Duration  time.Duration
}

// FormatTelegram renders the report as a Telegram-safe message.
// The output is capped at 4000 characters to stay within the Telegram limit.
func (r JobReport) FormatTelegram() string {
	var b strings.Builder

	// Status header
	switch r.Status {
	case "succeeded":
		b.WriteString("✅ ")
	case "timed_out":
		b.WriteString("⏰ ")
	default:
		b.WriteString("❌ ")
	}

	b.WriteString(fmt.Sprintf("*Scheduled job #%d* — %s\n", r.CronJobID, r.Status))

	if r.JobID != "" {
		b.WriteString(fmt.Sprintf("Job: `%s`\n", r.JobID))
	}

	b.WriteString(fmt.Sprintf("Type: %s\n", r.Type))

	if r.Duration > 0 {
		b.WriteString(fmt.Sprintf("Duration: %s\n", r.Duration.Round(time.Millisecond)))
	}

	// Body
	if r.Summary != "" {
		b.WriteString(fmt.Sprintf("\n%s\n", r.Summary))
	}

	if r.Output != "" {
		b.WriteString(fmt.Sprintf("\n```\n%s\n```\n", r.Output))
	}

	if r.Error != "" {
		b.WriteString(fmt.Sprintf("\nError: %s\n", r.Error))
	}

	msg := b.String()
	if len(msg) > 4000 {
		msg = msg[:4000] + "\n...(truncated)"
	}
	return msg
}

// buildReport constructs a JobReport from a completed cron-triggered durable job.
func buildReport(cronJob storage.CronJob, runtimeJobID string, started time.Time, result runtime.JobRunResult, runErr error) JobReport {
	status := "succeeded"
	errMsg := ""
	if runErr != nil {
		status = "failed"
		errMsg = runErr.Error()
	}

	output := ""
	for _, a := range result.Artifacts {
		if a.Type == "stdout" && a.Content != "" {
			output = a.Content
			break
		}
	}

	return JobReport{
		CronJobID: cronJob.ID,
		JobID:     runtimeJobID,
		Type:      cronJobType(cronJob),
		Task:      cronJob.Task,
		Status:    status,
		Summary:   result.Summary,
		Output:    output,
		Error:     errMsg,
		Duration:  time.Since(started),
	}
}

func cronJobType(j storage.CronJob) string {
	if j.Type == "" {
		return "llm"
	}
	return j.Type
}
