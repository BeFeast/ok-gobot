package cron

import (
	"strings"
	"testing"
	"time"
)

func TestJobReportFormatTelegramSuccess(t *testing.T) {
	t.Parallel()

	report := JobReport{
		CronJobID:  7,
		Expression: "0 */5 * * * *",
		Task:       "check disk usage",
		JobType:    "exec",
		Status:     "succeeded",
		Summary:    "/dev/sda1 42% used",
		Duration:   1234 * time.Millisecond,
		JobID:      "job-abc123",
	}

	msg := report.FormatTelegram()

	if !strings.Contains(msg, "✅") {
		t.Error("expected success emoji")
	}
	if !strings.Contains(msg, "#7") {
		t.Error("expected cron job ID")
	}
	if !strings.Contains(msg, "job-abc123") {
		t.Error("expected durable job ID")
	}
	if !strings.Contains(msg, "exec") {
		t.Error("expected job type")
	}
	if !strings.Contains(msg, "/dev/sda1 42% used") {
		t.Error("expected summary in output")
	}
	if !strings.Contains(msg, "1.234s") {
		t.Error("expected duration")
	}
}

func TestJobReportFormatTelegramFailure(t *testing.T) {
	t.Parallel()

	report := JobReport{
		CronJobID:  3,
		Expression: "0 0 * * * *",
		Task:       "run backup",
		JobType:    "llm",
		Status:     "failed",
		Error:      "connection refused",
		Duration:   500 * time.Millisecond,
	}

	msg := report.FormatTelegram()

	if !strings.Contains(msg, "❌") {
		t.Error("expected failure emoji")
	}
	if !strings.Contains(msg, "connection refused") {
		t.Error("expected error message")
	}
}

func TestJobReportFormatTelegramTimeout(t *testing.T) {
	t.Parallel()

	report := JobReport{
		CronJobID:  5,
		Expression: "0 0 3 * * *",
		Task:       "heavy analysis",
		JobType:    "llm",
		Status:     "timed_out",
		Error:      "context deadline exceeded",
		Duration:   15 * time.Minute,
	}

	msg := report.FormatTelegram()

	if !strings.Contains(msg, "⏰") {
		t.Error("expected timeout emoji")
	}
}

func TestJobReportFormatTelegramTruncation(t *testing.T) {
	t.Parallel()

	report := JobReport{
		CronJobID:  1,
		Expression: "0 * * * * *",
		Task:       "big output",
		JobType:    "exec",
		Status:     "succeeded",
		Summary:    strings.Repeat("x", 5000),
	}

	msg := report.FormatTelegram()

	if len(msg) > telegramMaxLen+50 { // allow room for truncation suffix
		t.Errorf("message too long: %d chars", len(msg))
	}
	if !strings.Contains(msg, "truncated") {
		t.Error("expected truncation marker")
	}
}

func TestJobReportFormatTelegramNoJobID(t *testing.T) {
	t.Parallel()

	report := JobReport{
		CronJobID:  2,
		Expression: "0 0 * * * *",
		Task:       "legacy run",
		JobType:    "exec",
		Status:     "succeeded",
		Summary:    "ok",
	}

	msg := report.FormatTelegram()

	// Should not contain backtick-wrapped empty string
	if strings.Contains(msg, "``") {
		t.Error("should not render empty job ID")
	}
}
