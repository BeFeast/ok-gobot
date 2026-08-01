package bot

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	artifactview "ok-gobot/internal/artifacts"
	"ok-gobot/internal/storage"
)

func displayRoleWorker(worker string) string {
	worker = strings.TrimSpace(worker)
	if worker == "" {
		return "default"
	}
	return worker
}

func formatRoleJobAck(jobID, roleName, worker string) string {
	return fmt.Sprintf("⚙️ Role job started\nJob: `%s`\nRole: *%s*\nWorker tier: `%s`\n\nUse `/job %s` for durable status and artifacts.",
		jobID, roleName, displayRoleWorker(worker), jobID)
}

func (b *Bot) waitAndNotifyRoleJob(chat *telebot.Chat, jobID string, maxWait time.Duration) {
	if b == nil || b.api == nil || b.store == nil || chat == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	if maxWait <= 0 {
		maxWait = 10 * time.Minute
	}
	ctx := b.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(maxWait)
		defer deadline.Stop()
		pollErrors := 0

		for {
			select {
			case <-ticker.C:
				job, err := b.store.GetJob(jobID)
				if err != nil {
					pollErrors++
					if pollErrors == 1 || pollErrors%10 == 0 {
						log.Printf("[role] failed to poll job %s for Telegram notification (%d consecutive errors): %v", jobID, pollErrors, err)
					}
					continue
				}
				pollErrors = 0
				if job != nil && isTerminalRoleJobStatus(job.Status) {
					b.sendRoleJobFinalNotification(chat, *job)
					return
				}
			case <-deadline.C:
				log.Printf("[role] timed out waiting for job %s terminal notification", jobID)
				return
			case <-ctx.Done():
				log.Printf("[role] bot shutting down; cancelling notification wait for job %s", jobID)
				return
			}
		}
	}()
}

func (b *Bot) sendRoleJobFinalNotification(chat *telebot.Chat, job storage.Job) {
	infos := b.roleJobArtifactInfos(job.JobID)
	text := formatRoleJobFinal(job, infos)
	if _, err := b.api.Send(chat, text, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
		log.Printf("[role] markdown final notification failed for job %s, retrying as plain text: %v", job.JobID, err)
		if _, err2 := b.api.Send(chat, text); err2 != nil {
			log.Printf("[role] failed to send final notification for job %s: markdown=%v plain=%v", job.JobID, err, err2)
			return
		}
	}

	if info, ok := artifactview.FirstSafeLocalImage(infos); ok {
		b.sendRoleJobProofArtifact(chat, job.JobID, info)
	}
}

func (b *Bot) roleJobArtifactInfos(jobID string) []artifactview.Info {
	rows, err := b.store.ListJobArtifacts(jobID, 20)
	if err != nil {
		log.Printf("[role] failed to list artifacts for job %s: %v", jobID, err)
		return nil
	}
	return artifactview.NewSerializer(b.artifactRoots, "").SerializeAll(rows)
}

func formatRoleJobFinal(job storage.Job, infos []artifactview.Info) string {
	var sb strings.Builder
	sb.WriteString(roleJobFinalHeading(job.Status))
	sb.WriteString(fmt.Sprintf("\nJob: `%s`", job.JobID))
	if roleName := strings.TrimSpace(job.RoleName); roleName != "" {
		sb.WriteString(fmt.Sprintf("\nRole: *%s*", roleName))
	}
	if worker := displayRoleWorker(job.Worker); worker != "" {
		sb.WriteString(fmt.Sprintf("\nWorker tier: `%s`", worker))
	}

	if job.Status == "succeeded" {
		summary := truncateTelegramField(job.Summary, 700)
		if summary == "" {
			summary = "Completed successfully."
		}
		sb.WriteString("\n\nSummary:\n")
		sb.WriteString(summary)
	} else {
		sb.WriteString("\n\nReason: ")
		sb.WriteString(truncateTelegramField(roleJobFailureReason(job), 700))
		if summary := truncateTelegramField(job.Summary, 500); summary != "" {
			sb.WriteString("\n\nSummary:\n")
			sb.WriteString(summary)
		}
	}

	if len(infos) > 0 {
		sb.WriteString("\n\nProof artifacts:")
		for _, hint := range artifactview.FormatProofHints(infos, 6) {
			sb.WriteString("\n- ")
			sb.WriteString(hint)
		}
		if _, ok := artifactview.FirstSafeLocalImage(infos); ok {
			sb.WriteString("\nSafe image proof will be attached separately when Telegram accepts the file.")
		}
	}

	sb.WriteString(fmt.Sprintf("\n\nUse `/job %s` for durable status and artifacts.", job.JobID))
	return sb.String()
}

func isTerminalRoleJobStatus(status string) bool {
	switch status {
	case "succeeded", "failed", "preflight_failed", "cancelled", "timed_out", "budget_exceeded":
		return true
	default:
		return false
	}
}

func roleJobFinalHeading(status string) string {
	switch status {
	case "succeeded":
		return "✅ *Role job completed*"
	case "timed_out":
		return "⏰ *Role job timed out*"
	case "preflight_failed":
		return "❌ *Role job blocked by preflight*"
	case "cancelled":
		return "🛑 *Role job cancelled*"
	case "budget_exceeded":
		return "🛑 *Role job budget exceeded*"
	default:
		return "❌ *Role job failed*"
	}
}

func roleJobFailureReason(job storage.Job) string {
	if job.Status == "budget_exceeded" {
		if reason := strings.TrimSpace(job.LimitReason); reason != "" {
			return "budget exceeded: " + reason
		}
		return "budget exceeded"
	}
	if errMsg := strings.TrimSpace(job.Error); errMsg != "" {
		return errMsg
	}
	switch job.Status {
	case "timed_out":
		return "job timed out"
	case "cancelled":
		return "job was cancelled"
	case "failed":
		return "job failed"
	default:
		return job.Status
	}
}

func (b *Bot) sendRoleJobProofArtifact(chat *telebot.Chat, jobID string, info artifactview.Info) {
	if strings.TrimSpace(info.Path) == "" {
		return
	}
	stat, err := os.Stat(info.Path)
	if err != nil || stat.IsDir() {
		b.sendRoleJobAttachmentFallback(chat, jobID, info)
		return
	}

	caption := fmt.Sprintf("Proof artifact #%d for job %s", info.ID, jobID)
	if label := strings.TrimSpace(info.Label); label != "" {
		caption = fmt.Sprintf("%s: %s", caption, truncateTelegramField(label, 120))
	}
	photo := &telebot.Photo{File: telebot.FromDisk(info.Path), Caption: caption}
	if _, err := b.api.Send(chat, photo); err == nil {
		return
	}
	doc := &telebot.Document{File: telebot.FromDisk(info.Path), Caption: caption}
	if _, err := b.api.Send(chat, doc); err != nil {
		log.Printf("[role] failed to attach proof artifact id=%d for job %s", info.ID, jobID)
		b.sendRoleJobAttachmentFallback(chat, jobID, info)
	}
}

func (b *Bot) sendRoleJobAttachmentFallback(chat *telebot.Chat, jobID string, info artifactview.Info) {
	msg := fmt.Sprintf("Proof image artifact #%d could not be attached. Use `/job %s` for durable artifact details.", info.ID, jobID)
	if _, err := b.api.Send(chat, msg, &telebot.SendOptions{ParseMode: telebot.ModeMarkdown}); err != nil {
		log.Printf("[role] failed to send proof attachment fallback for job %s", jobID)
	}
}

func truncateTelegramField(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
