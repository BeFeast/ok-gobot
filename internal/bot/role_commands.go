package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/role"
	"ok-gobot/internal/rolejob"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

// loadBotRoles returns all roles available to the bot (disk + bundled).
func (b *Bot) loadBotRoles() ([]*role.Manifest, error) {
	var all []*role.Manifest

	if b.rolesPath != "" {
		manifests, errs := role.LoadDirLenient(b.rolesPath)
		for _, e := range errs {
			log.Printf("[roles] skipping invalid manifest: %v", e)
		}
		all = append(all, manifests...)
	}

	bundled, err := role.LoadBundled()
	if err != nil {
		return all, fmt.Errorf("loading bundled roles: %w", err)
	}

	seen := make(map[string]bool, len(all))
	for _, m := range all {
		seen[m.Name] = true
	}
	for _, m := range bundled {
		if !seen[m.Name] {
			all = append(all, m)
		}
	}

	return all, nil
}

// findBotRole looks up a role by name.
func (b *Bot) findBotRole(name string) (*role.Manifest, error) {
	roles, err := b.loadBotRoles()
	if err != nil {
		return nil, err
	}
	for _, m := range roles {
		if m.Name == name {
			return m, nil
		}
	}
	return nil, fmt.Errorf("role %q not found", name)
}

// handleRolesCommand handles /roles — list available roles.
func (b *Bot) handleRolesCommand(c telebot.Context) error {
	roles, err := b.loadBotRoles()
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to load roles: %v", err))
	}

	if len(roles) == 0 {
		return c.Send("No roles available.")
	}

	var sb strings.Builder
	sb.WriteString("*Available Roles:*\n\n")
	for _, m := range roles {
		schedule := ""
		if m.Schedule != "" {
			schedule = fmt.Sprintf(" (scheduled: `%s`)", m.Schedule)
		}
		worker := m.Worker
		if worker == "" {
			worker = "default"
		}
		source := "bundled"
		if m.SourcePath != "" {
			source = "disk"
		}
		sb.WriteString(fmt.Sprintf("*%s* — worker: %s, source: %s%s\n", m.Name, worker, source, schedule))
	}
	sb.WriteString("\nRun with `/role_run <name> <task>`.")
	if findRoleInList(roles, "prototype-builder") != nil {
		sb.WriteString("\nDemo: `/role_run prototype-builder Build a blue 3D rocket launch simulator`")
	}

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleRoleCommand handles /role <name> — show role details.
func (b *Bot) handleRoleCommand(c telebot.Context) error {
	name := strings.TrimSpace(c.Message().Payload)
	if name == "" {
		return c.Send("Usage: `/role <name>`", &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	m, err := b.findBotRole(name)
	if err != nil {
		return c.Send(fmt.Sprintf("Role %q not found.", name))
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("*Role: %s*\n\n", m.Name))
	worker := m.Worker
	if worker == "" {
		worker = "(default)"
	}
	sb.WriteString(fmt.Sprintf("Worker: `%s`\n", worker))
	sb.WriteString(fmt.Sprintf("Approval: `%s`\n", m.Approval))
	if m.Schedule != "" {
		sb.WriteString(fmt.Sprintf("Schedule: `%s`\n", m.Schedule))
	}
	if len(m.Tools) > 0 {
		sb.WriteString(fmt.Sprintf("Tools: `%s`\n", strings.Join(m.Tools, ", ")))
	}
	if m.SourcePath != "" {
		sb.WriteString("Source: disk\n")
	} else {
		sb.WriteString("Source: bundled\n")
	}

	// Truncate prompt for Telegram display.
	prompt := m.Prompt
	if len(prompt) > 500 {
		prompt = prompt[:497] + "..."
	}
	sb.WriteString(fmt.Sprintf("\n```\n%s\n```", prompt))

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleRoleRunCommand handles /role_run <name> [input] — run a role as a durable job.
func (b *Bot) handleRoleRunCommand(c telebot.Context) error {
	if !b.authManager.IsAdmin(c.Sender().ID) {
		return c.Send("This command is only available to administrators.")
	}

	payload := strings.TrimSpace(c.Message().Payload)
	if payload == "" {
		return c.Send("Usage: `/role_run <name> [input]`", &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	parts := strings.SplitN(payload, " ", 2)
	name := parts[0]
	input := ""
	if len(parts) > 1 {
		input = strings.TrimSpace(parts[1])
	}

	m, err := b.findBotRole(name)
	if err != nil {
		return c.Send(fmt.Sprintf("Role %q not found.", name))
	}

	sessionKey := rolejob.NewSessionKey("role", c.Chat().ID, m.Name)
	js := runtime.NewJobService(b.store)
	spec := rolejob.BuildSpec(m, input, string(sessionKey), "", "")
	job, err := js.StartDetached(context.Background(), spec, b.roleJobRunner(c.Chat(), m, input, sessionKey))
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to start role job: %v", err))
	}

	worker := spec.Worker
	if worker == "" {
		worker = "default"
	}
	return c.Send(fmt.Sprintf("Job started: `%s`\nRole: *%s*\nWorker: %s\nBudget: %d tool calls / %s\n\nUse `/job %s` to check status.",
		job.JobID, m.Name, worker, spec.MaxToolCalls, spec.Timeout, job.JobID),
		&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

func (b *Bot) roleJobRunner(chat *telebot.Chat, m *role.Manifest, input string, sessionKey agent.SessionKey) runtime.JobRunner {
	return func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		_ = svc.AppendEvent(job.JobID, runtime.JobEventProgress, "role runtime submitted", map[string]any{
			"role":        m.Name,
			"session_key": string(sessionKey),
		})
		result, err := rolejob.RunWithHub(ctx, b.hub, m, input, rolejob.RunOptions{
			SessionKey: sessionKey,
			ChatID:     chat.ID,
			OnToolEvent: func(event agent.ToolEvent) {
				switch event.Type {
				case agent.ToolEventStarted:
					_ = b.store.IncrementJobToolCallCount(job.JobID)
					_ = svc.AppendEvent(job.JobID, runtime.JobEventProgress, "tool started: "+event.ToolName, map[string]any{
						"tool": event.ToolName,
					})
				case agent.ToolEventFinished:
					msg := "tool finished: " + event.ToolName
					if event.Err != nil {
						msg = "tool failed: " + event.ToolName
					}
					_ = svc.AppendEvent(job.JobID, runtime.JobEventProgress, msg, map[string]any{
						"tool": event.ToolName,
					})
				}
			},
		})
		if err != nil {
			b.sendRoleJobFailure(chat, job.JobID, err)
			return result, err
		}
		b.sendRoleJobProof(chat, job.JobID, m.Name, result)
		return result, nil
	}
}

func (b *Bot) sendRoleJobFailure(chat *telebot.Chat, jobID string, err error) {
	msg := fmt.Sprintf("Role job failed: %s\n%s\n\nUse /job %s for details.", jobID, err.Error(), jobID)
	if _, sendErr := b.api.Send(chat, msg); sendErr != nil {
		log.Printf("[roles] failed to send role job failure for %s: %v", jobID, sendErr)
	}
}

func (b *Bot) sendRoleJobProof(chat *telebot.Chat, jobID, roleName string, result runtime.JobRunResult) {
	summary := result.Summary
	if len(summary) > 1200 {
		summary = summary[:1197] + "..."
	}
	msg := fmt.Sprintf("Role job completed: %s\nRole: %s\n\n%s\n\nUse /job %s for artifacts.", jobID, roleName, summary, jobID)
	if _, err := b.api.Send(chat, msg); err != nil {
		log.Printf("[roles] failed to send role job proof for %s: %v", jobID, err)
	}
	for _, artifact := range result.Artifacts {
		path, ok := rolejob.IsLocalImageArtifact(artifact)
		if !ok {
			continue
		}
		if err := b.SendPhotoToChat(chat.ID, path, "Proof screenshot for "+jobID); err != nil {
			log.Printf("[roles] failed to send proof screenshot %s for %s: %v", path, jobID, err)
		}
		return
	}
}

// handleTGJobsCommand handles /jobs — list recent durable jobs.
func (b *Bot) handleTGJobsCommand(c telebot.Context) error {
	jobs, err := b.store.ListJobs(20)
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to list jobs: %v", err))
	}

	if len(jobs) == 0 {
		return c.Send("No jobs found.")
	}

	var sb strings.Builder
	sb.WriteString("*Recent Jobs:*\n\n")
	for _, j := range jobs {
		desc := j.Description
		if len(desc) > 40 {
			desc = desc[:37] + "..."
		}
		icon := jobStatusIcon(j.Status)
		sb.WriteString(fmt.Sprintf("%s `%s` %s — %s\n", icon, j.JobID, j.Status, desc))
	}
	sb.WriteString("\nUse `/job <id>` for details.")

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

// handleTGJobCommand handles /job <id> — show job details.
func (b *Bot) handleTGJobCommand(c telebot.Context) error {
	jobID := strings.TrimSpace(c.Message().Payload)
	if jobID == "" {
		return c.Send("Usage: `/job <id>`", &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	job, err := b.store.GetJob(jobID)
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to get job: %v", err))
	}
	if job == nil {
		return c.Send(fmt.Sprintf("Job `%s` not found.", jobID), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	var sb strings.Builder
	icon := jobStatusIcon(job.Status)
	sb.WriteString(fmt.Sprintf("%s *Job %s*\n\n", icon, job.JobID))
	sb.WriteString(fmt.Sprintf("Status: `%s`\n", job.Status))
	sb.WriteString(fmt.Sprintf("Kind: `%s`\n", job.Kind))
	if job.Worker != "" {
		sb.WriteString(fmt.Sprintf("Worker: `%s`\n", job.Worker))
	}
	if job.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", job.Description))
	}
	sb.WriteString(fmt.Sprintf("Attempt: %d / %d\n", job.Attempt, job.MaxAttempts))
	if job.CancelRequested {
		sb.WriteString("Cancel requested: yes\n")
	}

	// Duration
	if job.StartedAt != "" && job.CompletedAt != "" {
		if start, err := parseJobTime(job.StartedAt); err == nil {
			if end, err := parseJobTime(job.CompletedAt); err == nil {
				sb.WriteString(fmt.Sprintf("Duration: %s\n", end.Sub(start).Truncate(time.Second)))
			}
		}
	}

	if job.Summary != "" {
		summary := job.Summary
		if len(summary) > 300 {
			summary = summary[:297] + "..."
		}
		sb.WriteString(fmt.Sprintf("\nSummary: %s\n", summary))
	}
	if job.Error != "" {
		errMsg := job.Error
		if len(errMsg) > 200 {
			errMsg = errMsg[:197] + "..."
		}
		sb.WriteString(fmt.Sprintf("\nError: %s\n", errMsg))
	}

	artifacts, err := b.store.ListJobArtifacts(job.JobID, 20)
	if err != nil {
		log.Printf("[jobs] failed to list artifacts for %s: %v", job.JobID, err)
	} else if len(artifacts) > 0 {
		sb.WriteString("\nArtifacts:\n")
		for _, a := range artifacts {
			sb.WriteString(fmt.Sprintf("- %s `%s` `%s`\n", artifactIcon(a.ArtifactType), a.ArtifactType, telegramCode(artifactLabel(a))))
		}
		if hasScreenshotArtifact(artifacts) {
			sb.WriteString("\nScreenshot proof is attached below when Telegram can read the local file.")
		}
		if job.Status == string(runtime.JobStatusSucceeded) {
			sb.WriteString(fmt.Sprintf("\nUse `/skill_suggest %s` to draft a reusable skill.", job.JobID))
		}
	}

	if err := c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
		return err
	}
	if path := firstLocalScreenshotArtifact(artifacts); path != "" {
		if err := b.SendPhotoToChat(c.Chat().ID, path, "Proof screenshot for "+job.JobID); err != nil {
			log.Printf("[jobs] failed to send screenshot artifact for %s: %v", job.JobID, err)
		}
	}
	return nil
}

// handleTGJobCancelCommand handles /job_cancel <id> — cancel a durable job.
func (b *Bot) handleTGJobCancelCommand(c telebot.Context) error {
	if !b.authManager.IsAdmin(c.Sender().ID) {
		return c.Send("This command is only available to administrators.")
	}

	jobID := strings.TrimSpace(c.Message().Payload)
	if jobID == "" {
		return c.Send("Usage: `/job_cancel <id>`", &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	job, err := b.store.GetJob(jobID)
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to get job: %v", err))
	}
	if job == nil {
		return c.Send(fmt.Sprintf("Job `%s` not found.", jobID), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	switch job.Status {
	case "succeeded", "cancelled", "timed_out":
		return c.Send(fmt.Sprintf("Job `%s` is already in terminal state: %s", jobID, job.Status),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}

	js := runtime.NewJobService(b.store)
	if err := js.Cancel(jobID); err != nil {
		return c.Send(fmt.Sprintf("Failed to cancel job: %v", err))
	}

	if job.Status == "pending" {
		return c.Send(fmt.Sprintf("Job `%s` cancelled.", jobID),
			&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
	}
	return c.Send(fmt.Sprintf("Cancellation requested for job `%s` (currently %s).", jobID, job.Status),
		&telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
}

func jobStatusIcon(status string) string {
	switch status {
	case "pending":
		return "⏳"
	case "running":
		return "🏃"
	case "succeeded":
		return "✅"
	case "failed":
		return "❌"
	case "cancelled":
		return "🛑"
	case "timed_out":
		return "⏰"
	case "budget_exceeded":
		return "🧯"
	default:
		return "🧾"
	}
}

func findRoleInList(roles []*role.Manifest, name string) *role.Manifest {
	for _, m := range roles {
		if m.Name == name {
			return m
		}
	}
	return nil
}

func artifactIcon(typ string) string {
	switch typ {
	case runtime.JobArtifactTypeScreenshot:
		return "📸"
	case runtime.JobArtifactTypeURL:
		return "🔗"
	case runtime.JobArtifactTypeFile:
		return "📄"
	case runtime.JobArtifactTypeTextReport:
		return "📝"
	default:
		return "📦"
	}
}

func artifactLabel(a storage.JobArtifact) string {
	if a.URI != "" {
		return a.URI
	}
	if a.Content != "" && len(a.Content) < 120 {
		return a.Content
	}
	if a.Name != "" {
		return a.Name
	}
	return "-"
}

func telegramCode(s string) string {
	s = strings.ReplaceAll(s, "`", "'")
	if len(s) > 160 {
		s = s[:157] + "..."
	}
	return s
}

func hasScreenshotArtifact(artifacts []storage.JobArtifact) bool {
	return firstLocalScreenshotArtifact(artifacts) != ""
}

func firstLocalScreenshotArtifact(artifacts []storage.JobArtifact) string {
	for _, a := range artifacts {
		if a.ArtifactType != runtime.JobArtifactTypeScreenshot {
			continue
		}
		path := rolejob.ArtifactPath(a.Content, a.URI)
		if path == "" {
			continue
		}
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func parseJobTime(ts string) (time.Time, error) {
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable time: %s", ts)
}
