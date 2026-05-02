package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/karaoke"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
)

const karaokeKind = "karaoke"

func (b *Bot) handleKaraokeCommand(c telebot.Context) error {
	rawURL := strings.TrimSpace(c.Message().Payload)
	if err := karaoke.ValidateYouTubeURL(rawURL); err != nil {
		return c.Send("Usage: /karaoke <youtube_url>")
	}
	if b.store == nil {
		return c.Send("Karaoke runtime is not available.")
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
		log.Printf("[karaoke] failed to persist delivery route for %s: %v", sessionKey, err)
	}

	cfg, err := b.karaokeRuntimeConfig()
	if err != nil {
		return c.Send(fmt.Sprintf("Karaoke config error: %v", err))
	}
	timeout := cfg.Timeout + time.Minute
	js := runtime.NewJobService(b.store)
	js.SetArtifactRoots(append([]string(nil), b.artifactRoots...))

	parentCtx := b.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	job, err := js.StartDetached(parentCtx, runtime.JobSpec{
		Kind:               karaokeKind,
		Worker:             "native",
		SessionKey:         sessionKey,
		DeliverySessionKey: sessionKey,
		Description:        "karaoke: " + rawURL,
		Timeout:            timeout,
		ArtifactRoots:      append([]string(nil), b.artifactRoots...),
	}, b.karaokeRunner(chat, rawURL, cfg))
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to start karaoke job: %v", err))
	}

	b.waitAndNotifyKaraokeJob(chat, job.JobID, timeout+time.Minute)
	return c.Send(fmt.Sprintf("Karaoke job started\nJob: %s\nURL: %s", job.JobID, rawURL))
}

func (b *Bot) karaokeRunner(chat *telebot.Chat, rawURL string, cfg karaoke.Config) runtime.JobRunner {
	return func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		submission, err := karaoke.Submit(ctx, rawURL, cfg)
		if err != nil {
			return runtime.JobRunResult{}, err
		}
		if err := svc.AppendEvent(job.JobID, runtime.JobEventProgress, "karaoke job accepted", map[string]any{
			"karaoke_job_id": submission.JobID,
			"status_url":     submission.StatusURL,
			"page_url":       submission.PageURL,
		}); err != nil {
			log.Printf("[karaoke] failed to append submit event for %s: %v", job.JobID, err)
		}
		if chat != nil && b.api != nil {
			if _, err := b.api.Send(chat, formatKaraokeAccepted(job.JobID, submission)); err != nil {
				log.Printf("[karaoke] failed to send accepted message for %s: %v", job.JobID, err)
			}
		}

		result, err := karaoke.Wait(ctx, submission, cfg)
		if err != nil {
			return runtime.JobRunResult{}, err
		}
		summary := formatKaraokeResult(result)
		return runtime.JobRunResult{
			Summary: summary,
			Artifacts: []runtime.JobArtifactSpec{
				{Name: "share-page", Type: runtime.JobArtifactTypeURL, URI: result.PageURL},
				{Name: "karaoke-mp3", Type: runtime.JobArtifactTypeURL, URI: result.KaraokeMP3URL},
				{Name: "vocals-mp3", Type: runtime.JobArtifactTypeURL, URI: result.VocalsMP3URL},
				{Name: "lyrics-text", Type: runtime.JobArtifactTypeURL, URI: result.LyricsTextURL},
			},
		}, nil
	}
}

func (b *Bot) karaokeRuntimeConfig() (karaoke.Config, error) {
	pollInterval := 10 * time.Second
	if raw := strings.TrimSpace(b.karaokeConfig.PollInterval); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return karaoke.Config{}, fmt.Errorf("karaoke.poll_interval: %w", err)
		}
		pollInterval = parsed
	}
	timeout := 3 * time.Hour
	if raw := strings.TrimSpace(b.karaokeConfig.Timeout); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return karaoke.Config{}, fmt.Errorf("karaoke.timeout: %w", err)
		}
		timeout = parsed
	}
	return karaoke.Config{
		ServiceURL:   b.karaokeConfig.ServiceURL,
		Token:        b.karaokeConfig.Token,
		Profile:      b.karaokeConfig.Profile,
		LyricsMode:   b.karaokeConfig.LyricsMode,
		PollInterval: pollInterval,
		Timeout:      timeout,
	}, nil
}

func formatKaraokeAccepted(jobID string, submission karaoke.Submission) string {
	monitor := firstNonEmptyString(submission.PageURL, submission.StatusURL)
	return fmt.Sprintf("Karaoke job accepted\nJob: %s\nKaraoke job: %s\nStatus: %s\nMonitor: %s",
		jobID, submission.JobID, firstNonEmptyString(submission.Status, "queued"), monitor)
}

func formatKaraokeResult(result karaoke.Result) string {
	return fmt.Sprintf("Karaoke completed\nTitle: %s\nJob: %s\nShare: %s\nKaraoke MP3: %s\nVocals MP3: %s\nLyrics: %s\nDuration: %s",
		result.Title,
		result.JobID,
		result.PageURL,
		result.KaraokeMP3URL,
		result.VocalsMP3URL,
		result.LyricsTextURL,
		result.ProcessingDurationDisplay)
}

func (b *Bot) waitAndNotifyKaraokeJob(chat *telebot.Chat, jobID string, maxWait time.Duration) {
	if b == nil || b.api == nil || b.store == nil || chat == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	if maxWait <= 0 {
		maxWait = 3*time.Hour + time.Minute
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
					log.Printf("[karaoke] failed to poll job %s: %v", jobID, err)
					continue
				}
				if job != nil && isTerminalRoleJobStatus(job.Status) {
					b.sendKaraokeFinalNotification(chat, *job)
					return
				}
			case <-deadline.C:
				log.Printf("[karaoke] timed out waiting for job %s terminal notification", jobID)
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (b *Bot) sendKaraokeFinalNotification(chat *telebot.Chat, job storage.Job) {
	if job.Status == string(runtime.JobStatusSucceeded) {
		if _, err := b.api.Send(chat, strings.TrimSpace(job.Summary)); err != nil {
			log.Printf("[karaoke] failed to send final notification for %s: %v", job.JobID, err)
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
	text := fmt.Sprintf("Karaoke failed\nJob: %s\nStatus: %s\nReason: %s", job.JobID, job.Status, truncateTelegramField(reason, 900))
	if _, err := b.api.Send(chat, text); err != nil {
		log.Printf("[karaoke] failed to send failure notification for %s: %v", job.JobID, err)
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
