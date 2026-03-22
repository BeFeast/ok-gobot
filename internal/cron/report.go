package cron

import (
	"fmt"
	"strings"
	"time"
)

const telegramMaxLen = 4000

// JobReport is a standardized report emitted after every cron-triggered job.
type JobReport struct {
	CronJobID  int64
	Expression string
	Task       string
	JobType    string // "llm" or "exec"
	Status     string // "succeeded", "failed", "timed_out"
	Summary    string // human-readable output or result
	Error      string
	Duration   time.Duration
	JobID      string // durable job ID (empty when running in legacy mode)
}

// FormatTelegram renders a Markdown report suitable for Telegram delivery.
func (r JobReport) FormatTelegram() string {
	var b strings.Builder

	switch r.Status {
	case "succeeded":
		fmt.Fprintf(&b, "✅ *Schedule #%d completed*", r.CronJobID)
	case "timed_out":
		fmt.Fprintf(&b, "⏰ *Schedule #%d timed out*", r.CronJobID)
	default:
		fmt.Fprintf(&b, "❌ *Schedule #%d failed*", r.CronJobID)
	}

	if r.JobID != "" {
		fmt.Fprintf(&b, "  `%s`", r.JobID)
	}

	fmt.Fprintf(&b, "\n`%s`  _%s_", r.Expression, r.JobType)

	if r.Duration > 0 {
		fmt.Fprintf(&b, "  (%s)", r.Duration.Round(time.Millisecond))
	}
	b.WriteString("\n")

	if r.Summary != "" {
		fmt.Fprintf(&b, "\n%s", r.Summary)
	}
	if r.Error != "" {
		fmt.Fprintf(&b, "\n\n*Error:* %s", r.Error)
	}

	msg := b.String()
	if len(msg) > telegramMaxLen {
		msg = msg[:telegramMaxLen] + "\n...(truncated)"
	}
	return msg
}
