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
	if rawURL == "" {
		return b.promptForCommandInput(c, commandInputVideoSummary)
	}
	b.clearPendingCommandInput(c)
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
	}, b.videoSummaryRunner(rawURL, cfg))
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to start video summary job: %v", err))
	}

	b.waitAndNotifyVideoSummaryJob(chat, job.JobID, timeout+time.Minute)
	return c.Send("🎬 Video summary started. I’ll send the Scribe link when it’s ready.")
}

func (b *Bot) videoSummaryRunner(rawURL string, cfg videosummary.Config) runtime.JobRunner {
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
		result, err := videosummary.WaitAndWrite(ctx, submission, cfg)
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
	if strings.TrimSpace(b.videoSummaryConfig.ScribeURL) == "" {
		return videosummary.Config{}, fmt.Errorf("video_summary.scribe_url is required")
	}
	if strings.TrimSpace(b.videoSummaryConfig.VaultDir) == "" {
		return videosummary.Config{}, fmt.Errorf("obsidian.vault_dir is required")
	}
	pollInterval := 5 * time.Second
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
		ScribeURL:     b.videoSummaryConfig.ScribeURL,
		APIToken:      b.videoSummaryConfig.APIToken,
		SummaryPrompt: b.videoSummaryConfig.SummaryPrompt,
		VaultDir:      b.videoSummaryConfig.VaultDir,
		PollInterval:  pollInterval,
		Timeout:       timeout,
	}, nil
}

func formatVideoSummaryResult(result videosummary.Result) string {
	title := escapeTelegramRichMarkdownText(strings.TrimSpace(result.Title))
	if title == "" {
		title = "Untitled video"
	}
	statusURL := escapeTelegramRichMarkdownLink(result.StatusURL)
	if statusURL == "" {
		return "✅ **Video summary ready**\n\n" + title
	}
	link := "[Open finished Scribe job](" + statusURL + ")"
	if duration := strings.TrimSpace(result.ProcessingDurationDisplay); duration != "" {
		link += " · " + escapeTelegramRichMarkdownText(duration)
	}
	return "✅ **Video summary ready**\n\n" + title + "\n" + link
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
		if _, err := b.sendTelegramRichMarkdown(chat, strings.TrimSpace(job.Summary)); err != nil {
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

func escapeTelegramRichMarkdownText(text string) string {
	const special = `\\` + "`*_{}[]<>()#+-.!|>"
	var escaped strings.Builder
	escaped.Grow(len(text))
	for _, r := range text {
		if strings.ContainsRune(special, r) {
			escaped.WriteByte('\\')
		}
		escaped.WriteRune(r)
	}
	return escaped.String()
}

func escapeTelegramRichMarkdownLink(rawURL string) string {
	return strings.ReplaceAll(strings.TrimSpace(rawURL), ")", "%29")
}
