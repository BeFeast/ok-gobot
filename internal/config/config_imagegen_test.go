package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSavePersistsImageGenConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		ConfigPath: configPath,
		ImageGen: ImageGenConfig{
			Model:   "gpt-image-2",
			Size:    "1536x1024",
			Quality: "high",
		},
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if loaded.ImageGen != cfg.ImageGen {
		t.Fatalf("ImageGen settings = %#v, want %#v", loaded.ImageGen, cfg.ImageGen)
	}
}

func TestImageGenDefaults(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	want := ImageGenConfig{Model: "gpt-image-2", Size: "1024x1024", Quality: ""}
	if loaded.ImageGen != want {
		t.Fatalf("ImageGen defaults = %#v, want %#v", loaded.ImageGen, want)
	}
}

func TestValidateRejectsInvalidImageGenSettings(t *testing.T) {
	base := func() *Config {
		return &Config{
			Telegram:    TelegramConfig{Token: "test-token"},
			AI:          AIConfig{APIKey: "test-key", Model: "test-model"},
			Auth:        AuthConfig{Mode: "open"},
			StoragePath: "/tmp/test.db",
			Maestro:     MaestroConfig{ReadyLabel: "ready"},
		}
	}

	cfg := base()
	cfg.ImageGen.Size = "huge"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "image_gen.size") {
		t.Fatalf("Validate() error = %v, want image_gen.size error", err)
	}

	cfg = base()
	cfg.ImageGen.Quality = "ultra"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "image_gen.quality") {
		t.Fatalf("Validate() error = %v, want image_gen.quality error", err)
	}

	for _, size := range []string{"0x1024", "-1024x1024", "x1024", "1024x"} {
		cfg = base()
		cfg.ImageGen.Size = size
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "image_gen.size") {
			t.Fatalf("Validate() with size %q error = %v, want image_gen.size error", size, err)
		}
	}

	for _, size := range []string{"", "auto", "1024x1024", "1536x1024"} {
		cfg = base()
		cfg.ImageGen.Size = size
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() with size %q error = %v, want nil", size, err)
		}
	}
}
