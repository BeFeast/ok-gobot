package bot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v4"
)

func TestBareVideoSummaryURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "watch URL", input: "https://www.youtube.com/watch?v=abc123", want: "https://www.youtube.com/watch?v=abc123", ok: true},
		{name: "short URL with whitespace", input: "  https://youtu.be/abc123\n", want: "https://youtu.be/abc123", ok: true},
		{name: "URL with request text", input: "summarize https://youtu.be/abc123", ok: false},
		{name: "X video", input: "https://x.com/example/status/2104238565043405021/video/1", want: "https://x.com/example/status/2104238565043405021/video/1", ok: true},
		{name: "Twitter status", input: "https://twitter.com/example/status/2104074408046604439?s=20", want: "https://twitter.com/example/status/2104074408046604439?s=20", ok: true},
		{name: "mobile Twitter", input: "https://mobile.twitter.com/example/status/123/video/2", want: "https://mobile.twitter.com/example/status/123/video/2", ok: true},
		{name: "Vimeo", input: "https://vimeo.com/123456", want: "https://vimeo.com/123456", ok: true},
		{name: "generic media", input: "https://example.com/video.mp4", want: "https://example.com/video.mp4", ok: true},
		{name: "unknown source delegated to Scribe", input: "https://example.com/video", want: "https://example.com/video", ok: true},
		{name: "prose X request", input: "what is https://x.com/example/status/123/video/1", ok: false},
		{name: "two URLs", input: "https://x.com/example/status/123/video/1 https://youtu.be/abc123", ok: false},
		{name: "missing host", input: "https:///video", ok: false},
		{name: "missing scheme", input: "example.com/video", ok: false},
		{name: "local file", input: "file:///tmp/video.mp4", ok: false},
		{name: "empty", input: "", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := bareVideoSummaryURL(tc.input)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("bareVideoSummaryURL(%q) = %q, %v; want %q, %v", tc.input, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestHandleNativeTextCommandRoutesBareYouTubeURL(t *testing.T) {
	b := &Bot{}
	c := &fakeContext{msg: &telebot.Message{
		Text:    "https://youtu.be/abc123",
		Payload: "preserve-me",
		Chat:    &telebot.Chat{ID: 10},
		Sender:  &telebot.User{ID: 20},
	}}

	handled, err := b.handleNativeTextCommand(c)
	if err != nil {
		t.Fatalf("handleNativeTextCommand: %v", err)
	}
	if !handled {
		t.Fatal("expected bare YouTube URL to be handled")
	}
	if len(c.sent) != 1 || c.sent[0] != "Video summary runtime is not available." {
		t.Fatalf("unexpected response: %#v", c.sent)
	}
	if c.msg.Payload != "preserve-me" {
		t.Fatalf("message payload was not restored: %q", c.msg.Payload)
	}
}

func TestHandleNativeTextCommandLeavesConversationalMessageForAgent(t *testing.T) {
	b := &Bot{}
	c := &fakeContext{msg: &telebot.Message{
		Text:   "what do you think about https://youtu.be/abc123?",
		Chat:   &telebot.Chat{ID: 10},
		Sender: &telebot.User{ID: 20},
	}}

	handled, err := b.handleNativeTextCommand(c)
	if err != nil || handled {
		t.Fatalf("handleNativeTextCommand = %v, %v; want false, nil", handled, err)
	}
	if len(c.sent) != 0 {
		t.Fatalf("unexpected response: %#v", c.sent)
	}
}

func TestHandleNativeTextCommandRoutesBareVideoURLs(t *testing.T) {
	for _, rawURL := range []string{
		"https://x.com/example/status/2104238565043405021/video/1",
		"https://x.com/example/status/2104074408046604439/video/1",
		"https://vimeo.com/123456",
		"https://example.com/video.mp4",
	} {
		t.Run(rawURL, func(t *testing.T) {
			b := &Bot{}
			c := &fakeContext{msg: &telebot.Message{Text: rawURL, Payload: "preserve-me", Chat: &telebot.Chat{ID: 10}, Sender: &telebot.User{ID: 20}}}
			handled, err := b.handleNativeTextCommand(c)
			if err != nil || !handled {
				t.Fatalf("route = %v, %v; want true, nil", handled, err)
			}
			if len(c.sent) != 1 || c.sent[0] != "Video summary runtime is not available." {
				t.Fatalf("expected native workflow response, got %#v", c.sent)
			}
			if c.msg.Payload != "preserve-me" {
				t.Fatalf("payload not restored: %q", c.msg.Payload)
			}
		})
	}
}

// Exercise both entrypoints through native submission, polling, and Telegram
// delivery. No LLM or real external service is involved.
func TestVideoURLNativeSubmissionAndDelivery(t *testing.T) {
	for _, tc := range []struct {
		name, source       string
		explicit, rejected bool
	}{
		{"first X bare", "https://x.com/example/status/2104238565043405021/video/1", false, false},
		{"second X bare", "https://x.com/example/status/2104074408046604439/video/1", false, false},
		{"Vimeo bare", "https://vimeo.com/123456", false, false},
		{"generic bare", "https://example.com/video.mp4", false, false},
		{"generic command", "https://example.com/video.mp4", true, false},
		{"unsupported bare", "https://example.com/article", false, true},
		{"unsupported command", "https://example.com/article", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posted := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/jobs":
					var body struct {
						URL string `json:"url"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					posted <- body.URL
					if tc.rejected {
						http.Error(w, "unsupported media URL", http.StatusUnprocessableEntity)
						return
					}
					_, _ = w.Write([]byte(`{"job_id":321,"status":"queued"}`))
				case "/jobs/321":
					_, _ = w.Write([]byte(`{"status":"completed","transcript":{"id":77,"title":"Test video"}}`))
				case "/transcripts/77/summary.md", "/transcripts/77/transcript.md":
					_, _ = w.Write([]byte("# Test artifact"))
				default:
					t.Errorf("unexpected endpoint: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			tg := newFakeTelegramAPI(t)
			api, err := telebot.NewBot(telebot.Settings{Token: "TEST", URL: tg.server.URL, Client: tg.server.Client(), Offline: true})
			if err != nil {
				t.Fatal(err)
			}
			api.Me = &telebot.User{ID: 1, Username: "testbot", IsBot: true}
			store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			vault := t.TempDir()
			b := &Bot{api: api, store: store, ctx: ctx, artifactRoots: []string{vault}, videoSummaryConfig: config.VideoSummaryConfig{ScribeURL: server.URL, VaultDir: vault, PollInterval: "1ms", Timeout: "2s"}}
			c := &fakeContext{msg: &telebot.Message{ID: 42, Text: tc.source, Payload: tc.source, Chat: &telebot.Chat{ID: 10, Type: telebot.ChatPrivate}, Sender: &telebot.User{ID: 20}}}
			if tc.explicit {
				err = b.handleVideoSummaryCommand(c)
			} else {
				var handled bool
				handled, err = b.handleNativeTextCommand(c)
				if !handled {
					t.Fatal("bare URL fell through to model")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-posted:
				if got != tc.source {
					t.Fatalf("submitted %q; want %q", got, tc.source)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("no Scribe POST")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				var queued, done, failed bool
				for _, r := range tg.snapshotRequests() {
					queued = queued || strings.Contains(r.Text, "/#/jobs/321")
					done = done || strings.Contains(r.Text, "/#/transcript/77")
					failed = failed || (strings.Contains(r.Text, "Video summary failed") && strings.Contains(r.Text, "unsupported media URL"))
				}
				if tc.rejected && failed {
					if queued || done {
						t.Fatal("rejected submission produced a success receipt")
					}
					break
				}
				if !tc.rejected && queued && done {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("delivery queued=%v done=%v failed=%v", queued, done, failed)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
