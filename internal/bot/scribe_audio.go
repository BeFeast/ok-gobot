package bot

import (
	"context"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/runtime"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/videosummary"
)

const scribeAudioKind = "scribe_audio"

// scribeAudioSkillName returns the configured audio-summary skill, or "" when
// the pipeline is off for this instance.
func (b *Bot) scribeAudioSkillName() string {
	return strings.TrimSpace(b.videoSummaryConfig.AudioSummarySkill)
}

// audioDocument reports whether a document is an audio file (an .m4a voice
// note forwarded from another messenger, for instance).
func audioDocument(doc *telebot.Document) bool {
	return doc != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(doc.MIME)), "audio/")
}

// scribeAudioSource describes the Telegram file behind an audio message.
type scribeAudioSource struct {
	file     *telebot.File
	label    string // "voice", "audio", "file"
	fileName string // upload name; Scribe shows it as the job title
}

func scribeAudioSourceOf(msg *telebot.Message) (scribeAudioSource, bool) {
	switch {
	case msg == nil:
		return scribeAudioSource{}, false
	case msg.Voice != nil:
		return scribeAudioSource{&msg.Voice.File, "voice", audioUploadName("", msg.Voice.MIME, fmt.Sprintf("voice-%d", msg.ID))}, true
	case msg.Audio != nil:
		return scribeAudioSource{&msg.Audio.File, "audio", audioUploadName(msg.Audio.FileName, msg.Audio.MIME, fmt.Sprintf("audio-%d", msg.ID))}, true
	case audioDocument(msg.Document):
		return scribeAudioSource{&msg.Document.File, "file", audioUploadName(msg.Document.FileName, msg.Document.MIME, fmt.Sprintf("audio-%d", msg.ID))}, true
	}
	return scribeAudioSource{}, false
}

var unsafeUploadNameChars = regexp.MustCompile(`[^\p{L}\p{N}._ -]+`)

// audioUploadName keeps the original file name when there is one, otherwise
// builds one from the MIME type so Scribe and ffprobe see a real extension.
func audioUploadName(original, mimeType, fallbackStem string) string {
	name := strings.TrimSpace(unsafeUploadNameChars.ReplaceAllString(filepath.Base(strings.TrimSpace(original)), "_"))
	if name == "" || name == "." || name == "_" {
		name = fallbackStem
	}
	if filepath.Ext(name) == "" {
		name += audioExtension(mimeType)
	}
	return name
}

func audioExtension(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "audio/ogg", "audio/opus", "":
		return ".ogg"
	case "audio/mp4", "audio/x-m4a", "audio/m4a", "audio/aac":
		return ".m4a"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	}
	if exts, err := mime.ExtensionsByType(mimeType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ".audio"
}

// findSkill returns the installed, usable skill with the given name.
func (b *Bot) findSkill(name string) (bootstrap.SkillEntry, bool) {
	if b.personality == nil {
		return bootstrap.SkillEntry{}, false
	}
	loader := b.personality.Loader()
	if loader == nil {
		return bootstrap.SkillEntry{}, false
	}
	for _, skill := range loader.Skills {
		if skill.Name == name && skill.Compatibility != bootstrap.SkillCompatibilityBlocked {
			return skill, true
		}
	}
	return bootstrap.SkillEntry{}, false
}

// handleScribeAudio is the audio pipeline enabled by
// video_summary.audio_summary_skill: download the attachment, upload it to
// Scribe, and once the transcript is done run an agent turn with the
// configured skill pre-selected. Whether the skill runs is decided here, not
// by the model.
func (b *Bot) handleScribeAudio(ctx context.Context, c telebot.Context) error {
	msg := c.Message()
	if !b.authManager.CheckAccess(msg.Sender.ID, msg.Chat.ID) {
		logDeniedAccess(msg.Sender.ID, msg.Sender.Username, msg.Chat.ID, string(msg.Chat.Type))
		return c.Send("🔒 Not authorized.")
	}
	if !b.groupManager.ShouldRespond(msg.Chat.ID, msg, b.api.Me.Username) {
		return nil
	}

	src, ok := scribeAudioSourceOf(msg)
	if !ok {
		return b.scribeAudioTerminal(c, "no_audio", "Получил сообщение без аудио — обработать не смогу.")
	}
	log.Printf("[scribe_audio] recv kind=%s chat=%d size=%dB name=%q", src.label, msg.Chat.ID, src.file.FileSize, src.fileName)

	if src.file.FileSize > maxTelegramBotFileBytes {
		return b.scribeAudioTerminal(c, "over_telegram_limit", fmt.Sprintf("Файл весит %.1f MB — Telegram отдаёт ботам файлы только до 20 MB, скачать его я не могу. Сожми запись или раздели её на части.", float64(src.file.FileSize)/(1024*1024)))
	}

	reader, err := b.api.File(src.file)
	if err != nil {
		log.Printf("[scribe_audio] getFile failed: %v", err)
		return b.scribeAudioTerminal(c, "getfile_failed", "Не смог скачать аудио из Telegram — попробуй ещё раз.")
	}
	defer reader.Close()

	dir, err := os.MkdirTemp("", "tg-audio-*")
	if err != nil {
		return b.scribeAudioTerminal(c, "tempfile_failed", "Не смог сохранить аудио во временный файл.")
	}
	path := filepath.Join(dir, src.fileName)
	if err := writeLimited(path, reader, maxTelegramBotFileBytes+1); err != nil {
		_ = os.RemoveAll(dir)
		log.Printf("[scribe_audio] download failed: %v", err)
		return b.scribeAudioTerminal(c, "download_failed", "Обрыв при скачивании аудио — попробуй ещё раз.")
	}
	return b.startScribeAudioJob(c, dir, path, src.label)
}

func writeLimited(path string, r io.Reader, limit int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(r, limit)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// startScribeAudioJob uploads a downloaded audio file to Scribe as a detached
// job and arranges the skill turn for when the transcript is done. It owns
// dir and removes it once the upload has been handed to Scribe.
func (b *Bot) startScribeAudioJob(c telebot.Context, dir, path, label string) error {
	owned := true
	defer func() {
		if owned {
			_ = os.RemoveAll(dir)
		}
	}()
	msg := c.Message()
	chat := c.Chat()

	skillName := b.scribeAudioSkillName()
	skill, ok := b.findSkill(skillName)
	if !ok {
		return b.scribeAudioTerminal(c, "skill_missing", fmt.Sprintf("Аудио получил, но skill %q не установлен — summary сделать не могу.", skillName))
	}
	cfg, err := b.scribeRuntimeConfig(false)
	if err != nil {
		log.Printf("[scribe_audio] scribe not configured: %v", err)
		return b.scribeAudioTerminal(c, "scribe_unconfigured", "Аудио получил, но Scribe не настроен (video_summary.scribe_url).")
	}
	if b.store == nil {
		return b.scribeAudioTerminal(c, "runtime_unavailable", "Аудио получил, но job runtime недоступен.")
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
		log.Printf("[scribe_audio] failed to persist delivery route: %v", err)
	}

	var sendOpts []interface{}
	if msg.ThreadID != 0 {
		sendOpts = append(sendOpts, &telebot.Topic{ThreadID: msg.ThreadID})
	}
	startMsg, startErr := b.api.Send(chat, scribeAudioStartNotice, sendOpts...)
	if startErr != nil {
		log.Printf("[scribe_audio] failed to send start notice: %v", startErr)
	}

	timeout := cfg.Timeout + time.Minute
	js := runtime.NewJobService(b.store)
	js.SetArtifactRoots(append([]string(nil), b.artifactRoots...))
	parentCtx := b.ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	transcripts := make(chan videosummary.Transcript, 1)
	sourceLabel := fmt.Sprintf("telegram-forward %s from chat %d msg %d", label, chat.ID, msg.ID)
	job, err := js.StartDetached(parentCtx, runtime.JobSpec{
		Kind:               scribeAudioKind,
		Worker:             "native",
		SessionKey:         string(sessionKey),
		DeliverySessionKey: string(sessionKey),
		Description:        "scribe audio → " + skill.Name,
		Timeout:            timeout,
		ArtifactRoots:      append([]string(nil), b.artifactRoots...),
	}, b.scribeAudioRunner(dir, path, sourceLabel, cfg, transcripts, func(queueLink string) {
		b.updateScribeAudioStartNotice(chat, startMsg, queueLink, sendOpts...)
	}))
	if err != nil {
		failText := fmt.Sprintf("Не смог запустить транскрипцию: %v", err)
		if startMsg != nil {
			if _, editErr := b.api.Edit(startMsg, failText); editErr == nil {
				log.Printf("[scribe_audio] terminal reason=job_start_failed chat=%d delivered=true err=%v", chat.ID, err)
				return nil
			}
		}
		return b.scribeAudioTerminal(c, "job_start_failed", failText)
	}
	owned = false // scribeAudioRunner owns dir after StartDetached succeeds.

	log.Printf("[scribe_audio] job started chat=%d job=%s skill=%s", chat.ID, job.JobID, skill.Name)
	b.waitScribeAudioJob(c, job.JobID, skill, transcripts, timeout+time.Minute)
	return nil
}

const scribeAudioStartNotice = "🎙 Аудио принял — отправляю в Scribe. Когда будет транскрипт, сделаю summary-страницу."

func scribeAudioQueuedNotice(queueLink string) string {
	return "🎙 Аудио в очереди Scribe: " + queueLink + "\nКогда будет транскрипт, сделаю summary-страницу."
}

func (b *Bot) updateScribeAudioStartNotice(chat *telebot.Chat, startMsg *telebot.Message, queueLink string, sendOpts ...interface{}) {
	if b == nil || b.api == nil || strings.TrimSpace(queueLink) == "" {
		return
	}
	text := scribeAudioQueuedNotice(queueLink)
	if startMsg != nil {
		if _, err := b.api.Edit(startMsg, text); err == nil {
			return
		}
	}
	if chat != nil {
		if _, err := b.api.Send(chat, text, sendOpts...); err != nil {
			log.Printf("[scribe_audio] failed to send queue link: %v", err)
		}
	}
}

// scribeAudioRunner uploads the file, waits for the transcript and publishes
// it on transcripts before returning, so the waiter can read it as soon as
// the job is marked succeeded.
func (b *Bot) scribeAudioRunner(dir, path, sourceLabel string, cfg videosummary.Config, transcripts chan<- videosummary.Transcript, onQueued func(queueLink string)) runtime.JobRunner {
	return func(ctx context.Context, job *storage.Job, svc *runtime.JobService) (runtime.JobRunResult, error) {
		submission, err := func() (videosummary.Submission, error) {
			defer os.RemoveAll(dir)
			return videosummary.SubmitUpload(ctx, path, sourceLabel, cfg)
		}()
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
			log.Printf("[scribe_audio] failed to append submit event for %s: %v", job.JobID, err)
		}
		transcript, err := videosummary.WaitForTranscript(ctx, submission, cfg)
		if err != nil {
			return runtime.JobRunResult{}, err
		}
		transcripts <- transcript
		return runtime.JobRunResult{
			Summary: fmt.Sprintf("Scribe transcript %d ready", transcript.ID),
			Artifacts: []runtime.JobArtifactSpec{
				{Name: "transcript-link", Type: runtime.JobArtifactTypeURL, URI: transcript.ScribeLink},
			},
		}, nil
	}
}

// waitScribeAudioJob watches the job and, on success, starts the skill turn;
// on failure it tells the chat why.
func (b *Bot) waitScribeAudioJob(c telebot.Context, jobID string, skill bootstrap.SkillEntry, transcripts <-chan videosummary.Transcript, maxWait time.Duration) {
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
					log.Printf("[scribe_audio] failed to poll job %s: %v", jobID, err)
					continue
				}
				if job == nil || !isTerminalRoleJobStatus(job.Status) {
					continue
				}
				if job.Status != string(runtime.JobStatusSucceeded) {
					b.sendScribeAudioFailure(c, *job)
					return
				}
				select {
				case transcript := <-transcripts:
					b.runScribeAudioSkillTurn(ctx, c, skill, transcript)
				default:
					log.Printf("[scribe_audio] job %s succeeded without a transcript", jobID)
					b.sendScribeAudioFailure(c, *job)
				}
				return
			case <-deadline.C:
				log.Printf("[scribe_audio] timed out waiting for job %s", jobID)
				_ = b.scribeAudioTerminal(c, "wait_timeout", "Scribe так и не закончил транскрипцию — summary не будет. Проверь очередь Scribe.")
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (b *Bot) runScribeAudioSkillTurn(ctx context.Context, c telebot.Context, skill bootstrap.SkillEntry, transcript videosummary.Transcript) {
	log.Printf("[scribe_audio] transcript=%d ready chat=%d — running skill %s", transcript.ID, c.Chat().ID, skill.Name)
	if err := b.dispatchAgentTurn(ctx, c, scribeAudioSkillPrompt(skill, transcript), nil); err != nil {
		log.Printf("[scribe_audio] skill turn dispatch failed: %v", err)
	}
}

// scribeAudioSkillPrompt is the agent-turn content after the transcript is
// done: the skill is fixed, the input is the transcript id.
func scribeAudioSkillPrompt(skill bootstrap.SkillEntry, transcript videosummary.Transcript) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The user sent an audio recording. Scribe has transcribed it: transcript id %d", transcript.ID)
	if title := strings.TrimSpace(transcript.Title); title != "" {
		fmt.Fprintf(&sb, " (%q)", title)
	}
	sb.WriteString(".\n")
	fmt.Fprintf(&sb, "Run the skill `%s` for this transcript now. Read %s with the `file` tool and follow it. Do not consider other skills.\n", skill.Name, skill.Path)
	fmt.Fprintf(&sb, "In SKILL.md, replace `{baseDir}` with %s.\n\n", filepath.Dir(skill.Path))
	sb.WriteString("User request:\n")
	fmt.Fprintf(&sb, "Scribe transcript %d", transcript.ID)
	return sb.String()
}

func (b *Bot) sendScribeAudioFailure(c telebot.Context, job storage.Job) {
	reason := strings.TrimSpace(job.Error)
	if reason == "" {
		reason = strings.TrimSpace(job.Summary)
	}
	if reason == "" {
		reason = job.Status
	}
	_ = b.scribeAudioTerminal(c, "job_"+job.Status, scribeAudioFailureText(reason))
}

// scribeAudioFailureText turns a Scribe/job error into a chat message with
// the cause first and the raw reason after it.
func scribeAudioFailureText(reason string) string {
	lower := strings.ToLower(reason)
	cause := "Scribe не смог обработать аудио."
	switch {
	case strings.Contains(lower, "returned 413"):
		cause = "Scribe отклонил файл: слишком большой для загрузки."
	case strings.Contains(lower, "returned 422"):
		cause = "Scribe не распознал файл как аудио (пустой или повреждённый)."
	case strings.Contains(lower, "returned 429"):
		cause = "Scribe сейчас перегружен (429) — попробуй позже."
	case strings.Contains(lower, "returned 503"):
		cause = "Загрузка файлов в Scribe сейчас недоступна (503)."
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "deadline exceeded"):
		cause = "Scribe не закончил транскрипцию вовремя."
	case strings.Contains(lower, "submit upload job"):
		cause = "Scribe недоступен."
	}
	return "❌ " + cause + "\nПричина: " + truncateTelegramField(reason, 600)
}

// scribeAudioTerminal sends a final message for an audio item that will not
// reach the skill, and journals the reason and delivery outcome once.
func (b *Bot) scribeAudioTerminal(c telebot.Context, reason, text string) error {
	chatID := int64(0)
	if chat := c.Chat(); chat != nil {
		chatID = chat.ID
	}
	if err := c.Send(text); err != nil {
		log.Printf("[scribe_audio] terminal reason=%s chat=%d delivered=false err=%v", reason, chatID, err)
		return err
	}
	log.Printf("[scribe_audio] terminal reason=%s chat=%d delivered=true", reason, chatID)
	return nil
}
