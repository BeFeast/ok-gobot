package bot

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/youtubekaraoke"
)

const youtubeKaraokeKind = "youtube_karaoke"

func (b *Bot) handleYouTubeKaraokeCommand(c telebot.Context) error {
	rawURL := strings.TrimSpace(c.Message().Payload)
	if err := youtubekaraoke.ValidateYouTubeURL(rawURL); err != nil {
		return c.Send("Usage: /youtube_karaoke <youtube_url>")
	}
	if b.store == nil {
		return c.Send("YouTube karaoke runtime is not available.")
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
		log.Printf("[youtube_karaoke] failed to persist delivery route for %s: %v", sessionKey, err)
	}

	cfg, err := b.youtubeKaraokeRuntimeConfig()
	if err != nil {
		return c.Send(fmt.Sprintf("YouTube karaoke config error: %v", err))
	}
	timeout := cfg.Timeout + time.Minute
	artifactRoots := append([]string(nil), b.artifactRoots...)
	artifactRoots = append(artifactRoots, cfg.OutputDir)
	js := runtime.NewJobService(b.store)
	js.SetArtifactRoots(artifactRoots)

	parentCtx := b.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	job, err := js.StartDetached(parentCtx, runtime.JobSpec{
		Kind:               youtubeKaraokeKind,
		Worker:             "native",
		SessionKey:         sessionKey,
		DeliverySessionKey: sessionKey,
		Description:        "youtube karaoke: " + rawURL,
		Timeout:            timeout,
		ArtifactRoots:      artifactRoots,
	}, b.youtubeKaraokeRunner(rawURL, cfg))
	if err != nil {
		return c.Send(fmt.Sprintf("Failed to start YouTube karaoke job: %v", err))
	}

	b.waitAndNotifyYouTubeKaraokeJob(chat, job.JobID, timeout+time.Minute)
	return c.Send(fmt.Sprintf("YouTube karaoke job started\nJob: %s\nURL: %s", job.JobID, rawURL))
}

func (b *Bot) youtubeKaraokeRunner(rawURL string, cfg youtubekaraoke.Config) runtime.JobRunner {
	return func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		result, err := youtubekaraoke.Run(ctx, rawURL, cfg, func(message string) {
			if err := svc.AppendEvent(job.JobID, runtime.JobEventProgress, message, nil); err != nil {
				log.Printf("[youtube_karaoke] failed to append progress event for %s: %v", job.JobID, err)
			}
		})
		if err != nil {
			return runtime.JobRunResult{}, err
		}
		summary := formatYouTubeKaraokeResult(job.JobID, result)
		return runtime.JobRunResult{
			Summary: summary,
			Artifacts: []runtime.JobArtifactSpec{
				{Name: "lyrics-lrc", Type: runtime.JobArtifactTypeFile, URI: result.LRCPath, MimeType: "text/plain"},
				{Name: "source-vtt", Type: runtime.JobArtifactTypeFile, URI: result.VTTPath, MimeType: "text/vtt"},
			},
			ArtifactRoots: []string{cfg.OutputDir},
		}, nil
	}
}

func (b *Bot) youtubeKaraokeRuntimeConfig() (youtubekaraoke.Config, error) {
	timeout := 30 * time.Minute
	if raw := strings.TrimSpace(b.youtubeKaraokeConfig.Timeout); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return youtubekaraoke.Config{}, fmt.Errorf("youtube_karaoke.timeout: %w", err)
		}
		timeout = parsed
	}
	return youtubekaraoke.Config{
		OutputDir:     b.youtubeKaraokeConfig.OutputDir,
		YTDLPPath:     b.youtubeKaraokeConfig.YTDLPPath,
		SubtitleLangs: b.youtubeKaraokeConfig.SubtitleLangs,
		Timeout:       timeout,
	}, nil
}

func formatYouTubeKaraokeResult(jobID string, result youtubekaraoke.Result) string {
	artifact := filepath.Base(result.LRCPath)
	if artifact == "." || artifact == string(filepath.Separator) {
		artifact = "lyrics LRC"
	}
	return fmt.Sprintf("YouTube karaoke completed\nTitle: %s\nJob: %s\nArtifact: %s\nLines: %d",
		result.Title, jobID, artifact, result.LineCount)
}

func (b *Bot) waitAndNotifyYouTubeKaraokeJob(chat *telebot.Chat, jobID string, maxWait time.Duration) {
	if b == nil || b.api == nil || b.store == nil || chat == nil || strings.TrimSpace(jobID) == "" {
		return
	}
	if maxWait <= 0 {
		maxWait = 31 * time.Minute
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
					log.Printf("[youtube_karaoke] failed to poll job %s: %v", jobID, err)
					continue
				}
				if job != nil && isTerminalRoleJobStatus(job.Status) {
					b.sendYouTubeKaraokeFinalNotification(chat, *job)
					return
				}
			case <-deadline.C:
				log.Printf("[youtube_karaoke] timed out waiting for job %s terminal notification", jobID)
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (b *Bot) sendYouTubeKaraokeFinalNotification(chat *telebot.Chat, job storage.Job) {
	if job.Status == string(runtime.JobStatusSucceeded) {
		path := b.youtubeKaraokeLRCArtifactPath(job.JobID)
		if path != "" {
			doc := &telebot.Document{
				File:     telebot.FromDisk(path),
				FileName: filepath.Base(path),
				MIME:     "text/plain",
				Caption:  truncateTelegramField(job.Summary, 900),
			}
			if _, err := b.api.Send(chat, doc); err == nil {
				return
			} else {
				log.Printf("[youtube_karaoke] failed to send LRC artifact for %s: %v", job.JobID, err)
			}
		}
		if _, err := b.api.Send(chat, strings.TrimSpace(job.Summary)); err != nil {
			log.Printf("[youtube_karaoke] failed to send final notification for %s: %v", job.JobID, err)
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
	text := fmt.Sprintf("YouTube karaoke failed\nJob: %s\nStatus: %s\nReason: %s", job.JobID, job.Status, truncateTelegramField(reason, 900))
	if _, err := b.api.Send(chat, text); err != nil {
		log.Printf("[youtube_karaoke] failed to send failure notification for %s: %v", job.JobID, err)
	}
}

func (b *Bot) youtubeKaraokeLRCArtifactPath(jobID string) string {
	if b == nil || b.store == nil {
		return ""
	}
	artifacts, err := b.store.ListJobArtifacts(jobID, 20)
	if err != nil {
		log.Printf("[youtube_karaoke] failed to list artifacts for %s: %v", jobID, err)
		return ""
	}
	for _, artifact := range artifacts {
		if artifact.Name == "lyrics-lrc" && strings.TrimSpace(artifact.URI) != "" {
			return artifact.URI
		}
	}
	for _, artifact := range artifacts {
		if artifact.ArtifactType == runtime.JobArtifactTypeFile && strings.HasSuffix(strings.ToLower(artifact.URI), ".lrc") {
			return artifact.URI
		}
	}
	return ""
}
