package bot

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/storage"
)

const maxSkillStatusJobs = 10

func (b *Bot) handleSkillStatusCommand(c telebot.Context) error {
	if b.store == nil {
		return c.Send("Skill job store is not available.")
	}

	arg := strings.TrimSpace(c.Message().Payload)
	if arg == "" || arg == "list" || arg == "recent" {
		jobs, err := b.store.ListJobs(50)
		if err != nil {
			return c.Send(fmt.Sprintf("Failed to list skill jobs: %v", err))
		}
		return c.Send(formatNativeSkillJobList(jobs, maxSkillStatusJobs))
	}

	job, err := b.store.GetJob(arg)
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to get skill job: %v", err))
	}
	if job == nil {
		return c.Send(fmt.Sprintf("Skill job not found: %s", arg))
	}
	if !isNativeSkillJobKind(job.Kind) {
		return c.Send(fmt.Sprintf("Job %s is not a native skill job.\nKind: %s\nUse /job %s for generic job details.", job.JobID, job.Kind, job.JobID))
	}

	artifacts, err := b.store.ListJobArtifacts(job.JobID, 20)
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to list skill job artifacts: %v", err))
	}
	return c.Send(formatNativeSkillJobDetails(*job, artifacts))
}

func formatNativeSkillJobList(jobs []storage.Job, limit int) string {
	if limit <= 0 {
		limit = maxSkillStatusJobs
	}

	var sb strings.Builder
	count := 0
	sb.WriteString("Native skill jobs:\n\n")
	for _, job := range jobs {
		if !isNativeSkillJobKind(job.Kind) {
			continue
		}
		count++
		sb.WriteString(fmt.Sprintf("%s %s — %s — %s\n", jobStatusIcon(job.Status), job.JobID, nativeSkillJobLabel(job.Kind), job.Status))
		if job.Description != "" {
			sb.WriteString(fmt.Sprintf("  %s\n", truncateTelegramField(job.Description, 90)))
		}
		if count >= limit {
			break
		}
	}

	if count == 0 {
		return "No native skill jobs found."
	}
	sb.WriteString("\nUse /skill_status <job_id> for links and recovery details.")
	return strings.TrimSpace(sb.String())
}

func formatNativeSkillJobDetails(job storage.Job, artifacts []storage.JobArtifact) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s %s job\n", jobStatusIcon(job.Status), nativeSkillJobLabel(job.Kind)))
	sb.WriteString(fmt.Sprintf("Job: %s\n", job.JobID))
	sb.WriteString(fmt.Sprintf("Status: %s\n", job.Status))
	if job.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", truncateTelegramField(job.Description, 160)))
	}
	if duration := nativeSkillJobDuration(job); duration != "" {
		sb.WriteString(fmt.Sprintf("Duration: %s\n", duration))
	}
	if job.Error != "" {
		sb.WriteString(fmt.Sprintf("Error: %s\n", truncateTelegramField(job.Error, 500)))
	}
	if job.Summary != "" {
		sb.WriteString("\nSummary:\n")
		sb.WriteString(truncateTelegramField(job.Summary, 900))
		sb.WriteString("\n")
	}

	if len(artifacts) > 0 {
		sb.WriteString("\nArtifacts:\n")
		for _, artifact := range artifacts {
			value := strings.TrimSpace(artifact.URI)
			if value == "" {
				value = strings.TrimSpace(artifact.Content)
			}
			if value == "" {
				continue
			}
			sb.WriteString(fmt.Sprintf("• %s: %s\n", artifact.Name, truncateTelegramField(value, 900)))
		}
	}
	sb.WriteString(fmt.Sprintf("\nRecovery: /skill_status %s", job.JobID))
	return strings.TrimSpace(sb.String())
}

func isNativeSkillJobKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case videoSummaryKind, karaokeKind:
		return true
	default:
		return false
	}
}

func nativeSkillJobLabel(kind string) string {
	switch strings.TrimSpace(kind) {
	case videoSummaryKind:
		return "video-summary"
	case karaokeKind:
		return "karaoke"
	default:
		return kind
	}
}

func nativeSkillJobDuration(job storage.Job) string {
	if job.StartedAt == "" || job.CompletedAt == "" {
		return ""
	}
	start, err := parseJobTime(job.StartedAt)
	if err != nil {
		return ""
	}
	end, err := parseJobTime(job.CompletedAt)
	if err != nil {
		return ""
	}
	if end.Before(start) {
		return ""
	}
	return end.Sub(start).Truncate(time.Second).String()
}
