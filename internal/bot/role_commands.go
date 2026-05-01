package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	artifactview "ok-gobot/internal/artifacts"
	"ok-gobot/internal/evidence"
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
		sb.WriteString(fmt.Sprintf("*%s* — worker: %s%s\n", m.Name, worker, schedule))
	}
	sb.WriteString("\nUse `/role <name>` for details or `/role_run <name> [input]` to run.")

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
	if b.hub == nil {
		return c.Send("Role runtime is not available.")
	}

	chat := c.Chat()
	message := c.Message()
	sender := c.Sender()
	chatID := int64(0)
	sessionKey := agent.SessionKey(fmt.Sprintf("role:%s", m.Name))
	if chat != nil {
		chatID = chat.ID
		sessionKey = sessionKeyForChat(chat)
	}
	deliverySessionKey := ""
	if chat != nil {
		route := storage.SessionRoute{
			SessionKey: string(sessionKey),
			Channel:    "telegram",
			ChatID:     chat.ID,
		}
		if message != nil {
			route.ReplyToMessageID = message.ID
		}
		if sender != nil {
			route.UserID = sender.ID
			route.Username = sender.Username
		}
		if err := b.store.SaveSessionRoute(route); err != nil {
			log.Printf("[roles] failed to persist delivery route for %s: %v", sessionKey, err)
		} else {
			deliverySessionKey = string(sessionKey)
		}
	}

	opts := rolejob.Options{
		SessionKey:         string(sessionKey),
		DeliverySessionKey: deliverySessionKey,
		Worker:             m.Worker,
		ChatID:             chatID,
		ArtifactRoots:      b.artifactRoots,
	}
	spec, err := rolejob.JobSpec(m, opts)
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to build role job: %v", err))
	}

	js := runtime.NewJobService(b.store)
	flusher := b.lifecycleFlush
	roleName := m.Name
	runner := rolejob.AgentJobRunner(b.hub, m, input, opts)
	parentCtx := b.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	job, err := js.StartDetached(parentCtx, spec, func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (result runtime.JobRunResult, runErr error) {
		defer func() {
			runLifecycleJobFlush(ctx, flusher, job, roleName, result, runErr)
		}()
		return runner(ctx, job, svc)
	})
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to start role job: %v", err))
	}

	notifyWait := 10 * time.Minute
	if spec.Timeout > 0 {
		notifyWait = spec.Timeout + time.Minute
	}
	b.waitAndNotifyRoleJob(chat, job.JobID, notifyWait)

	return c.Send(formatRoleJobAck(job.JobID, m.Name, spec.Worker), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
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
	if events, err := b.store.ListEvidenceEventsForJob(job.JobID, 6); err == nil && len(events) > 0 {
		sb.WriteString("\nEvidence:\n")
		sb.WriteString(evidence.RenderMarkdown(events, evidence.RenderOptions{Limit: 6, MaxSummaryRune: 180}))
		sb.WriteString("\n")
	}

	artifacts, err := b.store.ListJobArtifacts(job.JobID, 20)
	if err != nil {
		log.Printf("[jobs] failed to list artifacts for %s: %v", job.JobID, err)
	} else if len(artifacts) > 0 {
		serializer := artifactview.NewSerializer(b.artifactRoots, "")
		sb.WriteString("\nProof artifacts:\n")
		for _, hint := range artifactview.FormatProofHints(serializer.SerializeAll(artifacts), 8) {
			sb.WriteString(fmt.Sprintf("- %s\n", hint))
		}
	}

	return c.Send(sb.String(), &telebot.SendOptions{ParseMode: telebot.ModeMarkdown})
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
	default:
		return "🧾"
	}
}

// runLifecycleJobFlush stages a bounded memory draft for the job's terminal
// state. Called from the deferred path of a role-job runner so success,
// failure, timeout, and cancellation each leave a traceable note. Flush errors
// are logged but never propagated: a memory-write failure must not corrupt the
// job's recorded outcome.
func runLifecycleJobFlush(ctx context.Context, flusher *agent.LifecycleFlusher, job *storage.Job, roleName string, result runtime.JobRunResult, runErr error) {
	if flusher == nil || job == nil {
		return
	}

	rec := agent.FlushRecord{
		JobID:      job.JobID,
		SessionKey: job.SessionKey,
		RoleName:   roleName,
		Summary:    strings.TrimSpace(result.Summary),
	}
	for _, a := range result.Artifacts {
		entry := strings.TrimSpace(a.Name)
		if a.URI != "" {
			entry = fmt.Sprintf("%s (%s)", entry, a.URI)
		}
		if entry != "" {
			rec.Artifacts = append(rec.Artifacts, entry)
		}
	}

	switch {
	case runErr == nil:
		rec.Kind = agent.FlushKindJobSuccess
	case errors.Is(runErr, context.DeadlineExceeded) || (ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)):
		rec.Kind = agent.FlushKindJobTimeout
		if errors.Is(runErr, context.DeadlineExceeded) {
			rec.Detail = runErr.Error()
		} else {
			rec.Detail = ctx.Err().Error()
		}
	case errors.Is(runErr, context.Canceled) || (ctx != nil && errors.Is(ctx.Err(), context.Canceled)):
		rec.Kind = agent.FlushKindJobCancelled
		if errors.Is(runErr, context.Canceled) {
			rec.Detail = runErr.Error()
		} else {
			rec.Detail = ctx.Err().Error()
		}
	default:
		rec.Kind = agent.FlushKindJobFailure
		rec.Detail = runErr.Error()
	}

	if _, err := flusher.Flush(rec); err != nil {
		log.Printf("[lifecycle] job %s memory flush failed: %v", job.JobID, err)
	}
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
