package cron

import (
	"fmt"
	"strings"
	"time"

	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

const telegramMaxLen = 4000

// FormatReport builds a standardised Telegram-ready report for a completed
// cron-triggered job. Both LLM and exec jobs share the same envelope; only
// the inner content differs.
func FormatReport(cronJob storage.CronJob, jobID string, result runtime.JobRunResult, runErr error, elapsed time.Duration) string {
	var b strings.Builder

	typeTag := ""
	if cronJob.Type == "exec" {
		typeTag = " (exec)"
	}
	fmt.Fprintf(&b, "*Scheduled Report* \u2014 Cron #%d%s\n\n", cronJob.ID, typeTag)

	dur := formatDuration(elapsed)
	if runErr != nil {
		fmt.Fprintf(&b, "\u274c Failed after %s\n", dur)
	} else {
		fmt.Fprintf(&b, "\u2705 Completed in %s\n", dur)
	}
	if jobID != "" {
		fmt.Fprintf(&b, "Job: `%s`\n", jobID)
	}

	if runErr != nil {
		fmt.Fprintf(&b, "\nError: %s", runErr.Error())
	} else if result.Summary != "" {
		fmt.Fprintf(&b, "\n%s", result.Summary)
	}

	msg := b.String()
	if len(msg) > telegramMaxLen {
		msg = msg[:telegramMaxLen] + "\n...(truncated)"
	}
	return msg
}

func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	m := int(d.Minutes())
	s := int(d.Seconds()) % 60
	if s == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dm %ds", m, s)
}
