package bot

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/bootstrap"
	"ok-gobot/internal/config"
	"ok-gobot/internal/videosummary"
)

// lockedContext is a fakeContext safe to Send from the job waiter goroutine
// while the test reads what was sent.
type lockedContext struct {
	*fakeContext
	mu sync.Mutex
}

func (c *lockedContext) Send(what interface{}, opts ...interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fakeContext.Send(what, opts...)
}

func (c *lockedContext) sentText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.sent, "\n")
}

const oversize = 21 * 1024 * 1024

func oversizeAudioMessages() map[string]*telebot.Message {
	return map[string]*telebot.Message{
		"voice":    {ID: 1, Voice: &telebot.Voice{File: telebot.File{FileSize: oversize}, MIME: "audio/ogg"}},
		"audio":    {ID: 1, Audio: &telebot.Audio{File: telebot.File{FileSize: oversize}, MIME: "audio/mp4", FileName: "WhatsApp Audio.m4a"}},
		"document": {ID: 1, Document: &telebot.Document{File: telebot.File{FileSize: oversize}, MIME: "audio/mp4", FileName: "note.m4a"}},
	}
}

// dispatchAudioKind sends msg through the same entrypoint telebot uses for
// that kind.
func dispatchAudioKind(t *testing.T, b *Bot, kind string, c telebot.Context) {
	t.Helper()
	var err error
	switch kind {
	case "voice":
		err = b.handleVoiceRoute(t.Context(), c)
	case "audio":
		err = b.handleUnsupportedMessage(t.Context(), c)
	case "document":
		err = b.handleDocumentMessage(t.Context(), c)
	}
	if err != nil {
		t.Fatalf("%s handler: %v", kind, err)
	}
}

// With the flag on, every audio kind reaches the Scribe audio pipeline. The
// oversize branch is the first terminal one, so no network is involved.
func TestAudioKindsRouteToScribeAudioWhenEnabled(t *testing.T) {
	for kind, msg := range oversizeAudioMessages() {
		t.Run(kind, func(t *testing.T) {
			tg := newFakeTelegramAPI(t)
			b, aiClient := newChatPolicyTestBot(t, tg, "standby")
			b.videoSummaryConfig = config.VideoSummaryConfig{AudioSummarySkill: "scribe-summary"}
			ctx := newForwardTestContext(msg)

			out := captureLog(t, func() { dispatchAudioKind(t, b, kind, ctx) })
			if !strings.Contains(out, "[scribe_audio] terminal reason=over_telegram_limit") {
				t.Fatalf("log = %q, want the scribe_audio oversize terminal", out)
			}
			if len(ctx.sent) != 1 || !strings.Contains(ctx.sent[0], "20 MB") {
				t.Fatalf("sent = %#v, want one oversize message", ctx.sent)
			}
			if aiClient.calls() != 0 {
				t.Fatal("oversize audio must not start an agent turn")
			}
		})
	}
}

// With the flag off (Kobi), each kind keeps its previous behavior.
func TestAudioKindsKeepPreviousBehaviorWhenDisabled(t *testing.T) {
	for kind, msg := range oversizeAudioMessages() {
		t.Run(kind, func(t *testing.T) {
			tg := newFakeTelegramAPI(t)
			b, _ := newChatPolicyTestBot(t, tg, "standby")
			ctx := newForwardTestContext(msg)

			out := captureLog(t, func() { dispatchAudioKind(t, b, kind, ctx) })
			if strings.Contains(out, "[scribe_audio]") {
				t.Fatalf("disabled pipeline still logged scribe_audio: %q", out)
			}
			sent := strings.Join(ctx.sent, "\n")
			switch kind {
			case "voice":
				if !strings.Contains(sent, "Speech-to-text is not configured") {
					t.Fatalf("voice: sent = %q, want the STT reply", sent)
				}
			case "audio":
				if !strings.Contains(sent, "Получил аудио") || !strings.Contains(sent, "пока не умею") {
					t.Fatalf("audio: sent = %q, want the unsupported reply", sent)
				}
			case "document":
				if !strings.Contains(out, "[video_forward] terminal reason=over_telegram_limit") {
					t.Fatalf("document: log = %q, want the video_forward path", out)
				}
			}
		})
	}
}

func TestNonAudioDocumentIgnoresAudioPipeline(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	b, _ := newChatPolicyTestBot(t, tg, "standby")
	b.videoSummaryConfig = config.VideoSummaryConfig{AudioSummarySkill: "scribe-summary"}
	ctx := newForwardTestContext(&telebot.Message{ID: 1, Document: &telebot.Document{File: telebot.File{FileSize: oversize}, MIME: "video/mp4"}})

	out := captureLog(t, func() { dispatchAudioKind(t, b, "document", ctx) })
	if strings.Contains(out, "[scribe_audio]") || !strings.Contains(out, "[video_forward] terminal reason=over_telegram_limit") {
		t.Fatalf("video document must stay on the video path: %q", out)
	}
}

// fakeScribe serves the upload/poll API. uploadStatus != 0 rejects uploads.
func fakeScribe(t *testing.T, uploadStatus int, uploads chan<- string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/jobs/upload":
			file, header, err := r.FormFile("file")
			if err != nil {
				t.Errorf("upload without file part: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			data, _ := io.ReadAll(file)
			uploads <- fmt.Sprintf("%s:%s", header.Filename, data)
			if uploadStatus != 0 {
				http.Error(w, `{"detail":"media storage is not configured"}`, uploadStatus)
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"job_id":900,"status":"queued"}`))
		case r.URL.Path == "/jobs/900":
			_, _ = w.Write([]byte(`{"status":"done","transcript":{"id":631,"title":"WhatsApp voice note"}}`))
		default:
			t.Errorf("unexpected Scribe endpoint: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

func newScribeAudioTestBot(t *testing.T, scribeURL string) (*Bot, *fakeTelegramAPI, *policyAIClient, string) {
	t.Helper()
	tg := newFakeTelegramAPI(t)
	b, aiClient := newChatPolicyTestBot(t, tg, "standby")
	skillPath := writeTestSkill(t, b.personality.BasePath, "scribe-summary", "description: Summarize a Scribe transcript\n")
	if err := b.personality.Reload(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b.ctx = ctx
	b.videoSummaryConfig = config.VideoSummaryConfig{
		ScribeURL:         scribeURL,
		PollInterval:      "1ms",
		Timeout:           "5s",
		AudioSummarySkill: "scribe-summary",
	}
	return b, tg, aiClient, skillPath
}

func downloadedAudio(t *testing.T, name, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

// After Scribe reports done, the bot itself starts an agent turn with the
// configured skill and the transcript id; the model does not choose.
func TestScribeAudioDoneRunsSkillTurnWithTranscriptID(t *testing.T) {
	uploads := make(chan string, 1)
	scribe := fakeScribe(t, 0, uploads)
	defer scribe.Close()
	b, tg, aiClient, skillPath := newScribeAudioTestBot(t, scribe.URL)

	dir, path := downloadedAudio(t, "WhatsApp Audio.m4a", "m4a-bytes")
	c := &lockedContext{fakeContext: dmContext(42, "")}
	c.msg.Audio = &telebot.Audio{MIME: "audio/mp4", FileName: "WhatsApp Audio.m4a"}
	if err := b.startScribeAudioJob(c, dir, path, "audio"); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-uploads:
		if got != "WhatsApp Audio.m4a:m4a-bytes" {
			t.Fatalf("upload = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no Scribe upload")
	}
	tg.waitForText(t, policyAnswer, 5*time.Second)
	waitForChatIdle(t, b, 5150, 5*time.Second)

	for _, want := range []string{"transcript id 631", "skill `scribe-summary`", skillPath, "User request:\nScribe transcript 631"} {
		if !aiClient.sawUserText(want) {
			t.Errorf("agent turn missing %q; seen=%q", want, aiClient.seen)
		}
	}
	var queued bool
	for _, r := range tg.snapshotRequests() {
		queued = queued || strings.Contains(r.Text, "/#/jobs/900")
	}
	if !queued {
		t.Error("start notice was not updated with the Scribe queue link")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir %s not removed after upload: %v", dir, err)
	}
}

func TestScribeAudioUploadRejectedReportsCause(t *testing.T) {
	uploads := make(chan string, 1)
	scribe := fakeScribe(t, http.StatusServiceUnavailable, uploads)
	defer scribe.Close()
	b, _, aiClient, _ := newScribeAudioTestBot(t, scribe.URL)

	dir, path := downloadedAudio(t, "voice-1.ogg", "ogg")
	c := &lockedContext{fakeContext: dmContext(42, "")}
	if err := b.startScribeAudioJob(c, dir, path, "voice"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(c.sentText(), "503") {
		if time.Now().After(deadline) {
			t.Fatalf("no failure message; sent=%q", c.sentText())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(c.sentText(), "недоступна") {
		t.Fatalf("failure message lacks the cause: %q", c.sentText())
	}
	if aiClient.calls() != 0 {
		t.Fatal("failed upload must not start the skill turn")
	}
}

func TestScribeAudioMissingSkillFailsBeforeUpload(t *testing.T) {
	uploads := make(chan string, 1)
	scribe := fakeScribe(t, 0, uploads)
	defer scribe.Close()
	b, _, _, _ := newScribeAudioTestBot(t, scribe.URL)
	b.videoSummaryConfig.AudioSummarySkill = "not-installed"

	dir, path := downloadedAudio(t, "voice-1.ogg", "ogg")
	c := dmContext(42, "")
	if err := b.startScribeAudioJob(c, dir, path, "voice"); err != nil {
		t.Fatal(err)
	}
	if len(c.sent) != 1 || !strings.Contains(c.sent[0], `"not-installed"`) {
		t.Fatalf("sent = %#v", c.sent)
	}
	select {
	case got := <-uploads:
		t.Fatalf("uploaded %q although the skill is missing", got)
	default:
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp dir not removed: %v", err)
	}
}

func TestScribeAudioSkillPrompt(t *testing.T) {
	skill := bootstrap.SkillEntry{Name: "scribe-summary", Path: "/soul/skills/scribe-summary/SKILL.md"}
	got := scribeAudioSkillPrompt(skill, videosummary.Transcript{ID: 631, Title: "Voice note"})
	for _, want := range []string{"transcript id 631", `"Voice note"`, "skill `scribe-summary`", "/soul/skills/scribe-summary/SKILL.md", "`{baseDir}` with /soul/skills/scribe-summary", "User request:\nScribe transcript 631"} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q:\n%s", want, got)
		}
	}
}

func TestScribeAudioFailureText(t *testing.T) {
	cases := map[string]string{
		"scribe upload returned 413: too large":                "слишком большой",
		"scribe upload returned 422: not decodable media":      "не распознал",
		"scribe upload returned 429: slow down":                "перегружен",
		"scribe upload returned 503: storage off":              "недоступна",
		`scribe job timed out after 2h0m0s while status="run"`: "не закончил",
		"submit upload job: dial tcp: connection refused":      "Scribe недоступен",
		`scribe job ended with status "failed": whisper crash`: "не смог обработать",
	}
	for reason, want := range cases {
		got := scribeAudioFailureText(reason)
		if !strings.Contains(got, want) || !strings.Contains(got, reason) {
			t.Errorf("scribeAudioFailureText(%q) = %q, want %q and the raw reason", reason, got, want)
		}
	}
}

func TestAudioUploadName(t *testing.T) {
	cases := []struct{ original, mime, want string }{
		{"WhatsApp Audio 2026-10-06.m4a", "audio/mp4", "WhatsApp Audio 2026-10-06.m4a"},
		{"", "audio/ogg", "voice-7.ogg"},
		{"", "audio/mp4", "voice-7.m4a"},
		{"../../etc/passwd", "audio/mpeg", "passwd.mp3"},
		{"note", "audio/x-m4a", "note.m4a"},
	}
	for _, tc := range cases {
		if got := audioUploadName(tc.original, tc.mime, "voice-7"); got != tc.want {
			t.Errorf("audioUploadName(%q, %q) = %q, want %q", tc.original, tc.mime, got, tc.want)
		}
	}
}
