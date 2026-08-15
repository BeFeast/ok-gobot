package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavePersistsInteractionFastLane(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		ConfigPath: configPath,
		AI: AIConfig{
			InteractionModel:    "gpt-5.6-luna",
			InteractionThinking: "low",
		},
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if loaded.AI.InteractionModel != "gpt-5.6-luna" || loaded.AI.InteractionThinking != "low" {
		t.Fatalf("fast lane = %q/%q, want gpt-5.6-luna/low", loaded.AI.InteractionModel, loaded.AI.InteractionThinking)
	}
}

func TestValidateRejectsInvalidInteractionThinking(t *testing.T) {
	cfg := &Config{
		Telegram:    TelegramConfig{Token: "test-token"},
		AI:          AIConfig{APIKey: "test-key", Model: "test-model"},
		Auth:        AuthConfig{Mode: "open"},
		StoragePath: "/tmp/test.db",
		Maestro:     MaestroConfig{ReadyLabel: "ready"},
	}

	// "adaptive" is rejected on purpose: the ChatGPT backend silently drops
	// it to the API default, which defeats the fast lane.
	for _, level := range []string{"turbo", "adaptive"} {
		cfg.AI.InteractionThinking = level
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ai.interaction_thinking") {
			t.Fatalf("Validate() with level %q error = %v, want ai.interaction_thinking error", level, err)
		}
	}

	for _, level := range []string{"", "off", "low", "medium", "high", "xhigh", "max"} {
		cfg.AI.InteractionThinking = level
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() with level %q error = %v, want nil", level, err)
		}
	}
}
