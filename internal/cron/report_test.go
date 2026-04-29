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

func TestJobReportFormatTelegramBudgetExceeded(t *testing.T) {
	t.Parallel()

	report := JobReport{
		CronJobID:   8,
		Expression:  "0 0 * * * *",
		Task:        "research task",
		JobType:     "llm",
		Status:      "budget_exceeded",
		LimitReason: "tool_call_limit",
		Summary:     "Completed 50/50 tool calls",
		Duration:    5 * time.Minute,
		JobID:       "job-xyz789",
	}

	msg := report.FormatTelegram()

	if !strings.Contains(msg, "🛑") {
		t.Error("expected budget exceeded emoji")
	}
	if !strings.Contains(msg, "budget exceeded") {
		t.Error("expected 'budget exceeded' in message")
	}
	if !strings.Contains(msg, "tool_call_limit") {
		t.Error("expected limit reason in message")
	}
	if !strings.Contains(msg, "Completed 50/50 tool calls") {
		t.Error("expected summary in output")
	}
}

func TestJobReportFormatTelegramCancelled(t *testing.T) {
	t.Parallel()

	report := JobReport{
		CronJobID:  9,
		Expression: "0 0 * * * *",
		Task:       "some task",
		JobType:    "llm",
		Status:     "cancelled",
		Error:      "user cancelled",
		Duration:   1 * time.Minute,
	}

	msg := report.FormatTelegram()

	if !strings.Contains(msg, "🚫") {
		t.Error("expected cancelled emoji")
	}
	if !strings.Contains(msg, "cancelled") {
		t.Error("expected 'cancelled' in message")
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
