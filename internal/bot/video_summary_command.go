package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/videosummary"
)

const videoSummaryKind = "video_summary"

func (b *Bot) handleVideoSummaryCommand(c telebot.Context) error {
	rawURL := strings.TrimSpace(c.Message().Payload)
	if err := videosummary.ValidateYouTubeURL(rawURL); err != nil {
		return c.Send("Usage: /video_summary <youtube_url>")
	}
	if b.store == nil {
		return c.Send("Video summary runtime is not available.")
	}

	chat := c.Chat()
	message := c.Message()
	sender := c.Sender()
	sessionKey := string(sessionKeyForChat(chat))
	if err := b.store.SaveSessionRoute(storage.SessionRoute{
		SessionKey:       sessionKey,
		Channel:          "telegram",
		ChatID:           chat.ID,
		ReplyToMessageID: message.ID,
		UserID:           sender.ID,
		Username:         sender.Username,
	}); err != nil {
		log.Printf("[video_summary] failed to persist delivery route for %s: %v", sessionKey, err)
	}

	cfg, err := b.videoSummaryRuntimeConfig()
	if err != nil {
		return c.Send(fmt.Sprintf("Video summary config error: %v", err))
	}
	timeout := cfg.Timeout + time.Minute
	js := runtime.NewJobService(b.store)
	js.SetArtifactRoots(append([]string(nil), b.artifactRoots...))

	parentCtx := b.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	job, err := js.StartDetached(parentCtx, runtime.JobSpec{
		Kind:               videoSummaryKind,
		Worker:             "native",
		SessionKey:         sessionKey,
		DeliverySessionKey: sessionKey,
		Description:        "video summary: " + rawURL,
		Timeout:            timeout,
		ArtifactRoots:      append([]string(nil), b.artifactRoots...),
	}, b.videoSummaryRunner(chat, rawURL, cfg))
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to start video summary job: %v", err))
	}

	b.waitAndNotifyVideoSummaryJob(chat, job.JobID, timeout+time.Minute)
	return c.Send(fmt.Sprintf("Video summary job started\nJob: %s\nURL: %s", job.JobID, rawURL))
}

func (b *Bot) videoSummaryRunner(chat *telebot.Chat, rawURL string, cfg videosummary.Config) runtime.JobRunner {
	return func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		submission, err := videosummary.Submit(ctx, rawURL, cfg)
		if err != nil {
			return runtime.JobRunResult{}, err
		}
		if err := svc.AppendEvent(job.JobID, runtime.JobEventProgress, "scribe job accepted", map[string]any{
			"scribe_job_id": submission.JobID,
			"status_url":    submission.StatusURL,
		}); err != nil {
			log.Printf("[video_summary] failed to append submit event for %s: %v", job.JobID, err)
		}
		if chat != nil && b.api != nil {
			if _, err := b.api.Send(chat, formatVideoSummaryAccepted(job.JobID, submission)); err != nil {
				log.Printf("[video_summary] failed to send accepted message for %s: %v", job.JobID, err)
			}
		}

		result, err := videosummary.WaitAndWriteWithProgress(ctx, submission, cfg, func(progress videosummary.Progress) {
			if err := svc.AppendEvent(job.JobID, runtime.JobEventProgress, "scribe progress", map[string]any{
				"scribe_job_id": progress.JobID,
				"status":        progress.Status,
				"title":         progress.Title,
			}); err != nil {
				log.Printf("[video_summary] failed to append progress event for %s: %v", job.JobID, err)
			}
			if chat != nil && b.api != nil {
				if _, err := b.api.Send(chat, formatVideoSummaryProgress(job.JobID, progress)); err != nil {
					log.Printf("[video_summary] failed to send progress message for %s: %v", job.JobID, err)
				}
			}
		})
		if err != nil {
			return runtime.JobRunResult{}, err
		}
		summary := formatVideoSummaryResult(result)
		return runtime.JobRunResult{
			Summary: summary,
			Artifacts: []runtime.JobArtifactSpec{
				{Name: "summary-link", Type: runtime.JobArtifactTypeURL, URI: result.SummaryLink},
				{Name: "transcript-link", Type: runtime.JobArtifactTypeURL, URI: result.TranscriptLink},
				{Name: "summary-file", Type: runtime.JobArtifactTypeFile, URI: result.SummaryPath, MimeType: "text/markdown"},
				{Name: "transcript-file", Type: runtime.JobArtifactTypeFile, URI: result.TranscriptPath, MimeType: "text/markdown"},
			},
			ArtifactRoots: []string{cfg.VaultDir},
		}, nil
	}
}

func (b *Bot) videoSummaryRuntimeConfig() (videosummary.Config, error) {
	pollInterval := 30 * time.Second
	if raw := strings.TrimSpace(b.videoSummaryConfig.PollInterval); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return videosummary.Config{}, fmt.Errorf("video_summary.poll_interval: %w", err)
		}
		pollInterval = parsed
	}
	timeout := 2 * time.Hour
	if raw := strings.TrimSpace(b.videoSummaryConfig.Timeout); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return videosummary.Config{}, fmt.Errorf("video_summary.timeout: %w", err)
		}
		timeout = parsed
	}
	return videosummary.Config{
		ScribeURL:    b.videoSummaryConfig.ScribeURL,
		VaultDir:     b.videoSummaryConfig.VaultDir,
		PollInterval: pollInterval,
		Timeout:      timeout,
	}, nil
}

func formatVideoSummaryAccepted(jobID string, submission videosummary.Submission) string {
	return fmt.Sprintf("Scribe accepted video summary\nJob: %s\nScribe job: %s\nStatus: %s\nTitle: %s",
		jobID, submission.JobID, submission.StatusURL, submission.Title)
}

func formatVideoSummaryProgress(jobID string, progress videosummary.Progress) string {
	return fmt.Sprintf("Video summary progress\nJob: %s\nScribe job: %s\nStatus: %s\nTitle: %s",
		jobID, progress.JobID, firstNonEmptyString(progress.Status, "running"), firstNonEmptyString(progress.Title, "-"))
}

func formatVideoSummaryResult(result videosummary.Result) string {
	return fmt.Sprintf("Video summary completed\nTitle: %s\nJob: %s\nSummary: %s\nTranscript: %s\nDuration: %s",
		result.Title, result.JobID, result.SummaryLink, result.TranscriptLink, result.ProcessingDurationDisplay)
}

func (b *Bot) waitAndNotifyVideoSummaryJob(chat *telebot.Chat, jobID string, maxWait time.Duration) {
	if b == nil || b.api == nil || b.store == nil || chat == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	if maxWait <= 0 {
		maxWait = 2*time.Hour + time.Minute
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

		for {
			select {
			case <-ticker.C:
				job, err := b.store.GetJob(jobID)
				if err != nil {
					log.Printf("[video_summary] failed to poll job %s: %v", jobID, err)
					continue
				}
				if job != nil && isTerminalRoleJobStatus(job.Status) {
					b.sendVideoSummaryFinalNotification(chat, *job)
					return
				}
			case <-deadline.C:
				log.Printf("[video_summary] timed out waiting for job %s terminal notification", jobID)
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (b *Bot) sendVideoSummaryFinalNotification(chat *telebot.Chat, job storage.Job) {
	if job.Status == string(runtime.JobStatusSucceeded) {
		if _, err := b.api.Send(chat, strings.TrimSpace(job.Summary)); err != nil {
			log.Printf("[video_summary] failed to send final notification for %s: %v", job.JobID, err)
		}
		return
	}
	reason := strings.TrimSpace(job.Error)
	if reason == "" {
		reason = strings.TrimSpace(job.Summary)
	}
	if reason == "" {
		reason = job.Status
	}
	text := fmt.Sprintf("Video summary failed\nJob: %s\nStatus: %s\nReason: %s", job.JobID, job.Status, truncateTelegramField(reason, 900))
	if _, err := b.api.Send(chat, text); err != nil {
		log.Printf("[video_summary] failed to send failure notification for %s: %v", job.JobID, err)
	}
}
