package bot

import (
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/config"
	"ok-gobot/internal/videosummary"
)

func TestVideoSummaryRuntimeConfigUsesScribeAndObsidianSettings(t *testing.T) {
	bot := &Bot{videoSummaryConfig: config.VideoSummaryConfig{
		ScribeURL: "https://scribe.example", APIToken: "test-token", SummaryPrompt: "Detailed",
		VaultDir: "/vault", PollInterval: "3s", Timeout: "45m",
	}}
	cfg, err := bot.videoSummaryRuntimeConfig()
	if err != nil {
		t.Fatalf("videoSummaryRuntimeConfig: %v", err)
	}
	if cfg.ScribeURL != "https://scribe.example" || cfg.APIToken != "test-token" || cfg.SummaryPrompt != "Detailed" || cfg.VaultDir != "/vault" {
		t.Fatalf("runtime config = %+v", cfg)
	}
	if cfg.PollInterval != 3*time.Second || cfg.Timeout != 45*time.Minute {
		t.Fatalf("runtime durations = %s / %s", cfg.PollInterval, cfg.Timeout)
	}
}

func TestFormatVideoSummaryResultUsesOnlyFinishedScribeLink(t *testing.T) {
	result := videosummary.Result{
		JobID:                     "514",
		StatusURL:                 "https://scribe.example/jobs/514",
		Title:                     "I made the PC I couldn't buy",
		SummaryLink:               "obsidian://summary",
		TranscriptLink:            "obsidian://transcript",
		ProcessingDurationDisplay: "3m 25s",
	}

	got := formatVideoSummaryResult(result)
	for _, want := range []string{
		"✅ **Video summary ready**",
		"I made the PC I couldn't buy",
		"[Open finished Scribe job](https://scribe.example/jobs/514) · 3m 25s",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("result missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"obsidian://", "Summary:", "Transcript:", "Job: 514"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("result contains %q:\n%s", unwanted, got)
		}
	}
}

func TestVideoSummaryRuntimeConfigRequiresServiceAndVault(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  config.VideoSummaryConfig
		want string
	}{
		"service": {cfg: config.VideoSummaryConfig{VaultDir: "/vault"}, want: "scribe_url"},
		"vault":   {cfg: config.VideoSummaryConfig{ScribeURL: "https://scribe.example"}, want: "obsidian.vault_dir"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (&Bot{videoSummaryConfig: tc.cfg}).videoSummaryRuntimeConfig()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}
