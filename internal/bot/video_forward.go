package bot

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/videosummary"
)

// Telegram's Bot API refuses getFile for anything over ~20 MB — larger
// forwards cannot be fetched by a bot at all, only by a user client.
const maxTelegramBotFileBytes = 20 * 1024 * 1024

// handleForwardedVideo transcribes a video that was sent/forwarded to the bot
// by uploading it to Scribe (the same pipeline /video_summary uses for
// YouTube links) and delivering the summary back into the chat.
func (b *Bot) handleForwardedVideo(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	chat := c.Chat()

	var file *telebot.File
	var label string
	switch {
	case msg.Video != nil:
		file, label = &msg.Video.File, "video"
	case msg.VideoNote != nil:
		file, label = &msg.VideoNote.File, "video note"
	default:
		return c.Send("Получил сообщение без видео-дорожки — обработать не смогу.")
	}

	if file.FileSize > maxTelegramBotFileBytes {
		return c.Send(fmt.Sprintf("Видео весит %.1f MB — Telegram отдаёт ботам файлы только до 20 MB. Если это YouTube-ролик, пришли ссылку (/video_summary), её лимит не касается.", float64(file.FileSize)/(1024*1024)))
	}

	reader, err := b.api.File(file)
	if err != nil {
		log.Printf("[video_forward] getFile failed: %v", err)
		return c.Send("Не смог скачать видео из Telegram — попробуй ещё раз.")
	}
	defer reader.Close()

	tmp, err := os.CreateTemp("", "tg-video-*.mp4")
	if err != nil {
		return c.Send("Не смог сохранить видео во временный файл.")
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, io.LimitReader(reader, maxTelegramBotFileBytes+1)); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		log.Printf("[video_forward] download failed: %v", err)
		return c.Send("Обрыв при скачивании видео — попробуй ещё раз.")
	}
	tmp.Close()
	tmpOwned := true
	defer func() {
		if tmpOwned {
			_ = os.Remove(tmpName)
		}
	}()

	probe, err := probeForwardedVideo(ctx, tmpName)
	if err != nil {
		log.Printf("[video_forward] media probe failed: %v", err)
		return c.Send("Не смог прочитать структуру видео — файл повреждён или ffprobe недоступен.")
	}

	sessionKey := sessionKeyForChat(chat)
	if err := b.store.SaveSessionRoute(storage.SessionRoute{
		SessionKey:       string(sessionKey),
		Channel:          "telegram",
		ChatID:           chat.ID,
		ReplyToMessageID: msg.ID,
		UserID:           msg.Sender.ID,
		Username:         msg.Sender.Username,
	}); err != nil {
		log.Printf("[video_forward] failed to persist delivery route: %v", err)
	}

	caption := msg.Caption
	if routeForwardedVideo(probe) == forwardedVideoRouteVision {
		if !ai.SupportsVision(b.ai) {
			return c.Send("В видео нет аудиодорожки, а текущая AI-модель не поддерживает visual analysis.")
		}
		frames, err := sampleSilentVideoFrames(ctx, tmpName, probe.DurationSecond)
		if err != nil {
			log.Printf("[video_forward] silent-video frame extraction failed: %v", err)
			return c.Send("В видео нет аудиодорожки, а извлечь кадры для visual analysis не удалось.")
		}
		content, visionContent := buildSilentVideoVisionContent(frames, probe.DurationSecond, caption)
		if err := b.store.SaveMessage(chat.ID, int64(msg.ID), msg.Sender.ID, msg.Sender.Username, content); err != nil {
			log.Printf("[video_forward] failed to save silent-video message: %v", err)
		}

		delivery := newTelegramDelivery(c)
		b.sendImmediateAck(delivery.Chat, msg.ID)
		session, err := b.store.GetSession(chat.ID)
		if err != nil {
			log.Printf("[video_forward] failed to get session: %v", err)
		}
		overrides := &agent.RunOverrides{Model: b.aiConfig.Model, TaskType: string(ai.TaskTypeVision)}
		b.runViaHubAsync(
			ctx,
			delivery,
			sessionKey,
			content,
			visionContent,
			session,
			overrides,
			"❌ Не удалось выполнить visual analysis этого видео.",
			"",
		)
		return nil
	}

	cfg, err := b.videoSummaryRuntimeConfig()
	if err != nil {
		log.Printf("[video_forward] scribe not configured: %v", err)
		return c.Send("Получил видео с аудиодорожкой, но транскрипция не настроена (video_summary.scribe_url).")
	}

	var sendOpts []interface{}
	if msg.ThreadID != 0 {
		sendOpts = append(sendOpts, &telebot.Topic{ThreadID: msg.ThreadID})
	}
	startMsg, startErr := b.api.Send(chat, "🎬 Видео принял — отправляю в транскрипцию, пришлю разбор.", sendOpts...)
	if startErr != nil {
		log.Printf("[video_forward] failed to send start notice: %v", startErr)
	}

	timeout := cfg.Timeout + time.Minute
	js := runtime.NewJobService(b.store)
	js.SetArtifactRoots(append([]string(nil), b.artifactRoots...))
	parentCtx := b.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	sourceLabel := fmt.Sprintf("telegram-forward %s from chat %d msg %d", label, chat.ID, msg.ID)
	job, err := js.StartDetached(parentCtx, runtime.JobSpec{
		Kind:               videoSummaryKind,
		Worker:             "native",
		SessionKey:         string(sessionKey),
		DeliverySessionKey: string(sessionKey),
		Description:        "video summary (telegram forward)",
		Timeout:            timeout,
		ArtifactRoots:      append([]string(nil), b.artifactRoots...),
	}, b.videoUploadRunner(tmpName, sourceLabel, caption, cfg, func(queueLink string) {
		b.updateVideoSummaryStartNotice(chat, startMsg, queueLink, sendOpts...)
	}))
	if err != nil {
		failText := fmt.Sprintf("Не смог запустить транскрипцию: %v", err)
		if startMsg != nil {
			if _, editErr := b.api.Edit(startMsg, failText); editErr == nil {
				return nil
			}
		}
		return c.Send(failText)
	}
	tmpOwned = false // videoUploadRunner owns the file after StartDetached succeeds.

	b.waitAndNotifyVideoSummaryJob(chat, job.JobID, timeout+time.Minute)
	return nil
}

// videoUploadRunner mirrors videoSummaryRunner for local files. It owns the
// temp file and removes it once the upload has been handed to Scribe.
func (b *Bot) videoUploadRunner(filePath, sourceLabel, caption string, cfg videosummary.Config, onQueued func(queueLink string)) runtime.JobRunner {
	return func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		defer os.Remove(filePath)
		submission, err := videosummary.SubmitUpload(ctx, filePath, sourceLabel, cfg)
		if err != nil {
			return runtime.JobRunResult{}, err
		}
		if onQueued != nil && submission.QueueLink != "" {
			onQueued(submission.QueueLink)
		}
		if err := svc.AppendEvent(job.JobID, runtime.JobEventProgress, "scribe upload accepted", map[string]any{
			"scribe_job_id": submission.JobID,
			"status_url":    submission.StatusURL,
			"queue_link":    submission.QueueLink,
		}); err != nil {
			log.Printf("[video_forward] failed to append submit event for %s: %v", job.JobID, err)
		}
		result, err := videosummary.WaitAndWrite(ctx, submission, cfg)
		if err != nil {
			return runtime.JobRunResult{}, err
		}
		summary := formatVideoSummaryResult(result)
		if caption != "" {
			summary = "Подпись к видео: " + caption + "\n\n" + summary
		}
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
