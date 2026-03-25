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
	AddExecJob(expression, task string, chatID int64, timeoutSeconds int) (int64, error)
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

func (c *CronTool) IsMutation(args ...string) bool {
	if len(args) == 0 {
		return false
	}
	command := args[0]
	return command == "add" || command == "remove" || command == "delete" || command == "toggle"
}

func (c *CronTool) IsVerification(args ...string) bool {
	if len(args) == 0 {
		return false
	}
	command := args[0]
	return command == "list"
}

func (c *CronTool) Description() string {
	return "Manage scheduled tasks (add, list, remove, toggle)"
}

// GetSchema returns the JSON Schema for cron tool parameters
func (c *CronTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"command": map[string]interface{}{
				"type":        "string",
				"description": "Cron command: add, list, remove, toggle",
				"enum":        []string{"add", "list", "remove", "toggle"},
			},
			"expression": map[string]interface{}{
				"type":        "string",
				"description": "Cron expression (for add command), e.g. '0 9 * * *'",
			},
			"task": map[string]interface{}{
				"type":        "string",
				"description": "Task description (for add command)",
			},
			"id": map[string]interface{}{
				"type":        "string",
				"description": "Job ID (for remove/toggle commands)",
			},
			"type": map[string]interface{}{
				"type":        "string",
				"description": "Job type: 'llm' (AI agent processes task) or 'exec' (direct shell execution). Default: llm",
				"enum":        []string{"llm", "exec"},
			},
			"timeout": map[string]interface{}{
				"type":        "string",
				"description": "Timeout in seconds for exec jobs (default: 900)",
			},
		},
		"required": []string{"command"},
	}
}

func (c *CronTool) Execute(ctx context.Context, args ...string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("usage: cron <add|list|remove|toggle> [args...]")
	}

	command := args[0]
	cmdArgs := args[1:]

	var result string
	var err error

	switch command {
	case "add":
		result, err = c.addJob(cmdArgs)
	case "list":
		result, err = c.listJobs()
	case "remove", "delete":
		result, err = c.removeJob(cmdArgs)
	case "toggle":
		result, err = c.toggleJob(cmdArgs)
	case "help":
		return c.help(), nil
	default:
		return "", fmt.Errorf("unknown command: %s\n\n%s", command, c.help())
	}

	if err == nil && c.IsMutation(args...) {
		res := ToolResult{
			Message: result,
			Evidence: &Evidence{
				Output: result,
			},
		}
		return res.String(), nil
	}

	return result, err
}

func (c *CronTool) addJob(args []string) (string, error) {
	if len(args) < 2 {
		return "", fmt.Errorf("usage: cron add <expression> <task> [--type exec] [--timeout 900]\n\nExamples:\n" +
			"  cron add \"0 9 * * *\" \"Good morning reminder\"\n" +
			"  cron add \"0 0 * * 1\" \"Weekly summary\"\n" +
			"  cron add \"30 3 * * *\" \"ssh shtrudel bash update.sh\" --type exec --timeout 900")
	}

	// Parse flags from args
	jobType := "llm"
	timeoutSec := 0
	var cleanArgs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--type" && i+1 < len(args) {
			jobType = args[i+1]
			i++
		} else if args[i] == "--timeout" && i+1 < len(args) {
			if v, err := strconv.Atoi(args[i+1]); err == nil {
				timeoutSec = v
			}
			i++
		} else {
			cleanArgs = append(cleanArgs, args[i])
		}
	}

	if len(cleanArgs) < 2 {
		return "", fmt.Errorf("expression and task are required")
	}

	expression := cleanArgs[0]
	task := strings.Join(cleanArgs[1:], " ")

	// Add seconds field if not present (5 fields -> 6 fields)
	fields := strings.Fields(expression)
	if len(fields) == 5 {
		expression = "0 " + expression
	}

	if c.scheduler == nil {
		return "", fmt.Errorf("scheduler not configured")
	}

	var jobID int64
	var err error
	if jobType == "exec" {
		if timeoutSec == 0 {
			timeoutSec = 900
		}
		jobID, err = c.scheduler.AddExecJob(expression, task, c.chatID, timeoutSec)
	} else {
		jobID, err = c.scheduler.AddJob(expression, task, c.chatID)
	}
	if err != nil {
		return "", fmt.Errorf("failed to add job: %w", err)
	}

	nextRun, _ := c.scheduler.GetNextRun(jobID)
	typeLabel := "AI"
	if jobType == "exec" {
		typeLabel = "exec"
	}
	return fmt.Sprintf("Job #%d created (%s)\nTask: %s\nSchedule: %s\nNext run: %s",
		jobID, typeLabel, task, expression, nextRun.Format("2006-01-02 15:04:05")), nil
}

func (c *CronTool) listJobs() (string, error) {
	if c.scheduler == nil {
		return "", fmt.Errorf("scheduler not configured")
	}

	jobs, err := c.scheduler.ListJobs()
	if err != nil {
		return "", fmt.Errorf("failed to list jobs: %w", err)
	}

	// Filter to only show jobs belonging to the current chat
	var filtered []storage.CronJob
	for _, job := range jobs {
		if c.chatID == 0 || job.ChatID == c.chatID {
			filtered = append(filtered, job)
		}
	}

	if len(filtered) == 0 {
		return "📅 No scheduled jobs", nil
	}

	var sb strings.Builder
	sb.WriteString("📅 Scheduled Jobs:\n\n")

	for _, job := range filtered {
		status := "✅"
		if !job.Enabled {
			status = "⏸️"
		}

		nextRun := "N/A"
		if t, err := c.scheduler.GetNextRun(job.ID); err == nil {
			nextRun = t.Format("2006-01-02 15:04")
		}

		typeTag := ""
		if job.Type == "exec" {
			typeTag = " [exec]"
		}
		sb.WriteString(fmt.Sprintf("%s #%d%s: %s\n", status, job.ID, typeTag, job.Task))
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

	jobs, err := c.scheduler.ListJobs()
	if err != nil {
		return "", fmt.Errorf("failed to list jobs: %w", err)
	}
	if err := c.verifyJobOwnership(jobID, jobs); err != nil {
		return "", err
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

	jobs, err := c.scheduler.ListJobs()
	if err != nil {
		return "", fmt.Errorf("failed to list jobs: %w", err)
	}
	if err := c.verifyJobOwnership(jobID, jobs); err != nil {
		return "", err
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

// verifyJobOwnership checks that the job belongs to the current chat.
// Accepts a pre-fetched jobs slice to avoid redundant DB queries.
func (c *CronTool) verifyJobOwnership(jobID int64, jobs []storage.CronJob) error {
	if c.chatID == 0 {
		return nil // no chat context — allow (e.g. admin/TUI)
	}
	for _, job := range jobs {
		if job.ID == jobID {
			if job.ChatID != c.chatID {
				return fmt.Errorf("job #%d does not belong to this chat", jobID)
			}
			return nil
		}
	}
	return fmt.Errorf("job #%d not found", jobID)
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
