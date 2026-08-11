package bot

import (
	"strings"
	"testing"
	"time"

	"ok-gobot/internal/config"
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
