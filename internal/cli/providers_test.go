package cli

import (
	"testing"

	"ok-gobot/internal/config"
)

func TestProviderStatus_ActiveWithKey(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Provider: "openrouter",
			APIKey:   "sk-test",
		},
	}
	if got := providerStatus(cfg, "openrouter"); got != "ok" {
		t.Errorf("expected ok, got %s", got)
	}
}

func TestProviderStatus_ActiveNoKey(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Provider: "openai",
		},
	}
	if got := providerStatus(cfg, "openai"); got != "no-key" {
		t.Errorf("expected no-key, got %s", got)
	}
}

func TestProviderStatus_Inactive(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Provider: "openrouter",
			APIKey:   "sk-test",
		},
	}
	if got := providerStatus(cfg, "openai"); got != "-" {
		t.Errorf("expected -, got %s", got)
	}
}

func TestProviderStatus_DroidAlwaysReady(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Provider: "openrouter",
		},
	}
	if got := providerStatus(cfg, "droid"); got != "ready" {
		t.Errorf("expected ready, got %s", got)
	}
}

func TestProviderStatus_DroidActiveOk(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Provider: "droid",
		},
	}
	if got := providerStatus(cfg, "droid"); got != "ok" {
		t.Errorf("expected ok, got %s", got)
	}
}

func TestProviderStatus_CustomNoURL(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Provider: "custom",
			APIKey:   "key",
		},
	}
	if got := providerStatus(cfg, "custom"); got != "no-url" {
		t.Errorf("expected no-url, got %s", got)
	}
}

func TestProviderStatus_CustomOk(t *testing.T) {
	cfg := &config.Config{
		AI: config.AIConfig{
			Provider: "custom",
			APIKey:   "key",
			BaseURL:  "http://localhost:8080",
		},
	}
	if got := providerStatus(cfg, "custom"); got != "ok" {
		t.Errorf("expected ok, got %s", got)
	}
}

func TestStatusColorCode(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"ok", colorGreen},
		{"ready", colorGreen},
		{"no-key", colorRed},
		{"no-url", colorRed},
		{"error", colorRed},
		{"-", colorYellow},
	}
	for _, tt := range tests {
		if got := statusColorCode(tt.status); got != tt.want {
			t.Errorf("statusColorCode(%q) = %q, want %q", tt.status, got, tt.want)
		}
	}
}
