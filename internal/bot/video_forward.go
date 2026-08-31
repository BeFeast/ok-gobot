package bot

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/videosummary"
)

// Telegram's Bot API refuses getFile for anything over ~20 MB — larger
// forwards cannot be fetched by a bot at all, only by a user client.
const maxTelegramBotFileBytes = 20 * 1024 * 1024

// transcribableDocument reports whether a document carries media Scribe can
// work with. Telegram sends anything uncompressed as a Document, so a video
// forwarded "as a file" arrives here rather than as msg.Video; without this it
// was never downloaded at all and the user got a description of the filename
// instead of a transcript.
func transcribableDocument(doc *telebot.Document) bool {
	if doc == nil {
		return false
	}
	mime := strings.ToLower(strings.TrimSpace(doc.MIME))
	return strings.HasPrefix(mime, "video/") || strings.HasPrefix(mime, "audio/")
}

// videoForwardTerminal is the single exit point for a forward that will not be
// transcribed: it records the reason in the journal and says whether the user
// actually saw the message.
//
// Before this, most branches were a bare `return c.Send(...)` with the send
// error discarded. On 2026-08-29 two forwarded videos produced one "[recv]
// video ... routing to scribe upload" line each and then nothing — no further
// log line, no job row, no reply — and which branch had been taken was not
// recoverable from the journal. Silence on both sides is the defect; every
// terminal path logs exactly once (issue #56).
func (b *Bot) videoForwardTerminal(c telebot.Context, reason, text string) error {
	chatID := int64(0)
	if chat := c.Chat(); chat != nil {
		chatID = chat.ID
	}
	if err := c.Send(text); err != nil {
		log.Printf("[video_forward] terminal reason=%s chat=%d delivered=false err=%v", reason, chatID, err)
		return err
	}
	log.Printf("[video_forward] terminal reason=%s chat=%d delivered=true", reason, chatID)
	return nil
}

// handleForwardedVideo transcribes media that was sent/forwarded to the bot by
// uploading it to Scribe (the same pipeline /video_summary uses for links) and
// delivering the summary back into the chat.
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
	case transcribableDocument(msg.Document):
		file, label = &msg.Document.File, "file"
	default:
		return b.videoForwardTerminal(c, "no_media_track", "Получил сообщение без видео-дорожки — обработать не смогу.")
	}

	if file.FileSize > maxTelegramBotFileBytes {
		return b.videoForwardTerminal(c, "over_telegram_limit", fmt.Sprintf("Файл весит %.1f MB — Telegram отдаёт ботам файлы только до 20 MB. Пришли ссылку на источник (/video_summary): лимит Telegram её не касается, и Scribe берёт не только YouTube.", float64(file.FileSize)/(1024*1024)))
	}

	reader, err := b.api.File(file)
	if err != nil {
		log.Printf("[video_forward] getFile failed: %v", err)
		return b.videoForwardTerminal(c, "getfile_failed", "Не смог скачать видео из Telegram — попробуй ещё раз.")
	}
	defer reader.Close()

	tmp, err := os.CreateTemp("", "tg-video-*.mp4")
	if err != nil {
		return b.videoForwardTerminal(c, "tempfile_failed", "Не смог сохранить видео во временный файл.")
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, io.LimitReader(reader, maxTelegramBotFileBytes+1)); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		log.Printf("[video_forward] download failed: %v", err)
		return b.videoForwardTerminal(c, "download_failed", "Обрыв при скачивании видео — попробуй ещё раз.")
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
		return b.videoForwardTerminal(c, "probe_failed", "Не смог прочитать структуру видео — файл повреждён или ffprobe недоступен.")
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
			return b.videoForwardTerminal(c, "vision_unsupported", "В видео нет аудиодорожки, а текущая AI-модель не поддерживает visual analysis.")
		}
		frames, err := sampleSilentVideoFrames(ctx, tmpName, probe.DurationSecond)
		if err != nil {
			log.Printf("[video_forward] silent-video frame extraction failed: %v", err)
			return b.videoForwardTerminal(c, "frame_extraction_failed", "В видео нет аудиодорожки, а извлечь кадры для visual analysis не удалось.")
		}
		delivery := newTelegramDelivery(c)
		b.sendImmediateAck(delivery.Chat, msg.ID)
		_, visionContent := buildSilentVideoVisionContent(frames, probe.DurationSecond, caption)
		b.runSilentVideoVisionAsync(
			ctx,
			delivery,
			visionContent,
		)
		log.Printf("[video_forward] terminal reason=vision_dispatched chat=%d delivered=true", chat.ID)
		return nil
	}

	cfg, err := b.videoSummaryRuntimeConfig()
	if err != nil {
		log.Printf("[video_forward] scribe not configured: %v", err)
		return b.videoForwardTerminal(c, "scribe_unconfigured", "Получил видео с аудиодорожкой, но транскрипция не настроена (video_summary.scribe_url).")
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
				log.Printf("[video_forward] terminal reason=job_start_failed chat=%d delivered=true err=%v", chat.ID, err)
				return nil
			}
		}
		return b.videoForwardTerminal(c, "job_start_failed", failText)
	}
	tmpOwned = false // videoUploadRunner owns the file after StartDetached succeeds.

	log.Printf("[video_forward] terminal reason=job_started chat=%d job=%s delivered=true", chat.ID, job.JobID)
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
