package bot

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/youtubekaraoke"
)

func TestYouTubeKaraokeCommandRejectsInvalidURL(t *testing.T) {
	bot := &Bot{}
	ctx := &fakeContext{msg: &telebot.Message{
		Payload: "https://example.com/not-youtube",
		Chat:    &telebot.Chat{ID: 123, Type: telebot.ChatPrivate},
		Sender:  &telebot.User{ID: 123, Username: "tester"},
	}}

	if err := bot.handleYouTubeKaraokeCommand(ctx); err != nil {
		t.Fatalf("handleYouTubeKaraokeCommand: %v", err)
	}
	if len(ctx.sent) != 1 || ctx.sent[0] != "Usage: /youtube_karaoke <youtube_url>" {
		t.Fatalf("unexpected response: %#v", ctx.sent)
	}
}

func TestYouTubeKaraokeRuntimeConfigParsesTimeout(t *testing.T) {
	bot := &Bot{youtubeKaraokeConfig: config.YouTubeKaraokeConfig{
		BaseURL:      "https://karaoke.example",
		APIToken:     "test-token",
		OutputDir:    "/tmp/karaoke",
		PollInterval: "3s",
		Timeout:      "45m",
	}}

	cfg, err := bot.youtubeKaraokeRuntimeConfig()
	if err != nil {
		t.Fatalf("youtubeKaraokeRuntimeConfig: %v", err)
	}
	if cfg.BaseURL != "https://karaoke.example" || cfg.APIToken != "test-token" || cfg.OutputDir != "/tmp/karaoke" {
		t.Fatalf("unexpected runtime config: %+v", cfg)
	}
	if cfg.PollInterval != 3*time.Second {
		t.Fatalf("PollInterval = %s, want 3s", cfg.PollInterval)
	}
	if cfg.Timeout != 45*time.Minute {
		t.Fatalf("Timeout = %s, want 45m", cfg.Timeout)
	}
}

func TestFormatYouTubeKaraokeResultDoesNotLeakDirectory(t *testing.T) {
	out := formatYouTubeKaraokeResult("job-123", youtubekaraoke.Result{
		Title:       "Song",
		KaraokePath: "/tmp/private/karaoke/karaoke.mp3",
		ShareURL:    "https://karaoke.example/share/token",
	})
	for _, want := range []string{"YouTube karaoke completed", "Song", "job-123", "karaoke.mp3", "https://karaoke.example/share/token"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
	if strings.Contains(out, "/tmp/private") {
		t.Fatalf("summary leaked local directory: %q", out)
	}
}

func TestYouTubeKaraokePrimaryArtifactPathPrefersKaraokeAudio(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.CreateJob(storage.Job{JobID: "job-karaoke", Kind: youtubeKaraokeKind, Status: "succeeded"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := store.AddJobArtifact(storage.JobArtifact{JobID: "job-karaoke", Name: "lyrics-lrc", ArtifactType: "file", URI: "/tmp/song.lrc"}); err != nil {
		t.Fatalf("AddJobArtifact lrc: %v", err)
	}
	if err := store.AddJobArtifact(storage.JobArtifact{JobID: "job-karaoke", Name: "karaoke-audio", ArtifactType: "file", URI: "/tmp/karaoke.mp3"}); err != nil {
		t.Fatalf("AddJobArtifact karaoke: %v", err)
	}

	bot := &Bot{store: store}
	if got := bot.youtubeKaraokePrimaryArtifactPath("job-karaoke"); got != "/tmp/karaoke.mp3" {
		t.Fatalf("youtubeKaraokePrimaryArtifactPath = %q", got)
	}
}
