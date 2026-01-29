package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ok-gobot/internal/storage"
)

// CronScheduler interface for the scheduler
type CronScheduler interface {
	AddJob(expression, task string, chatID int64) (int64, error)
	RemoveJob(jobID int64) error
	ToggleJob(jobID int64, enabled bool) error
	ListJobs() ([]storage.CronJob, error)
	GetNextRun(jobID int64) (time.Time, error)
}

// CronTool manages scheduled tasks
type CronTool struct {
	scheduler CronScheduler
	chatID    int64 // Current chat context
}

// NewCronTool creates a new cron tool
func NewCronTool(scheduler CronScheduler, chatID int64) *CronTool {
	return &CronTool{
		scheduler: scheduler,
		chatID:    chatID,
	}
}

func (c *CronTool) Name() string {
	return "cron"
}

func (c *CronTool) Description() string {
	return "Manage scheduled tasks (add, list, remove, toggle)"
}

func (c *CronTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: cron <add|list|remove|toggle> [args...]")
	}

	command := args[0]
	cmdArgs := args[1:]

	switch command {
	case "add":
		return c.addJob(cmdArgs)
	case "list":
		return c.listJobs()
	case "remove", "delete":
		return c.removeJob(cmdArgs)
	case "toggle":
		return c.toggleJob(cmdArgs)
	case "help":
		return c.help(), nil
	default:
		return "", fmt.Errorf("unknown command: %s\n\n%s", command, c.help())
	}
}

func (c *CronTool) addJob(args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: cron add <expression> <task>\n\nExamples:\n" +
			"  cron add \"0 9 * * *\" \"Good morning reminder\"\n" +
			"  cron add \"0 0 * * 1\" \"Weekly summary\"\n" +
			"  cron add \"*/30 * * * *\" \"Check emails\"")
	}

	expression := args[0]
	task := strings.Join(args[1:], " ")

	// Add seconds field if not present (5 fields -> 6 fields)
	fields := strings.Fields(expression)
	if len(fields) == 5 {
		expression = "0 " + expression // Add 0 seconds
	}

	if c.scheduler == nil {
		return "", fmt.Errorf("scheduler not configured")
	}

	jobID, err := c.scheduler.AddJob(expression, task, c.chatID)
	if err != nil {
		return "", fmt.Errorf("failed to add job: %w", err)
	}

	nextRun, _ := c.scheduler.GetNextRun(jobID)
	return fmt.Sprintf("✅ Job #%d created\nTask: %s\nSchedule: %s\nNext run: %s",
		jobID, task, expression, nextRun.Format("2006-01-02 15:04:05")), nil
}

func (c *CronTool) listJobs() (string, error) {
	if c.scheduler == nil {
		return "", fmt.Errorf("scheduler not configured")
	}

	jobs, err := c.scheduler.ListJobs()
	if err != nil {
		return "", fmt.Errorf("failed to list jobs: %w", err)
	}

	if len(jobs) == 0 {
		return "📅 No scheduled jobs", nil
	}

	var sb strings.Builder
	sb.WriteString("📅 Scheduled Jobs:\n\n")

	for _, job := range jobs {
		status := "✅"
		if !job.Enabled {
			status = "⏸️"
		}

		nextRun := "N/A"
		if t, err := c.scheduler.GetNextRun(job.ID); err == nil {
			nextRun = t.Format("2006-01-02 15:04")
		}

		sb.WriteString(fmt.Sprintf("%s #%d: %s\n", status, job.ID, job.Task))
		sb.WriteString(fmt.Sprintf("   Schedule: %s\n", job.Expression))
		sb.WriteString(fmt.Sprintf("   Next: %s\n\n", nextRun))
	}

	return sb.String(), nil
}

func (c *CronTool) removeJob(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: cron remove <job_id>")
	}

	jobID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid job ID: %s", args[0])
	}

	if c.scheduler == nil {
		return "", fmt.Errorf("scheduler not configured")
	}

	if err := c.scheduler.RemoveJob(jobID); err != nil {
		return "", fmt.Errorf("failed to remove job: %w", err)
	}

	return fmt.Sprintf("✅ Job #%d removed", jobID), nil
}

func (c *CronTool) toggleJob(args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: cron toggle <job_id> [on|off]")
	}

	jobID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return "", fmt.Errorf("invalid job ID: %s", args[0])
	}

	enabled := true
	if len(args) > 1 {
		switch strings.ToLower(args[1]) {
		case "off", "false", "0", "disable":
			enabled = false
		case "on", "true", "1", "enable":
			enabled = true
		}
	}

	if c.scheduler == nil {
		return "", fmt.Errorf("scheduler not configured")
	}

	if err := c.scheduler.ToggleJob(jobID, enabled); err != nil {
		return "", fmt.Errorf("failed to toggle job: %w", err)
	}

	status := "enabled"
	if !enabled {
		status = "disabled"
	}

	return fmt.Sprintf("✅ Job #%d %s", jobID, status), nil
}

func (c *CronTool) help() string {
	return `📅 Cron Tool - Manage scheduled tasks

Commands:
  cron add <expression> <task>  - Create a new scheduled task
  cron list                     - List all scheduled tasks
  cron remove <job_id>          - Remove a task
  cron toggle <job_id> [on|off] - Enable/disable a task

Cron Expression Format (5 fields):
  minute hour day-of-month month day-of-week

Examples:
  "0 9 * * *"     - Every day at 9:00 AM
  "0 9 * * 1"     - Every Monday at 9:00 AM
  "*/30 * * * *"  - Every 30 minutes
  "0 0 1 * *"     - First day of every month
  "0 18 * * 1-5"  - Weekdays at 6:00 PM`
}
