package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The AI key is supplied through the environment on hosts that keep secrets out
// of config.yaml. viper's Unmarshal only walks keys it knows from defaults or
// the config file, so without a registered default the variable resolves to ""
// — and an empty AI key means the client is never constructed, which surfaces
// as "no AI" rather than as a configuration error.
func TestAIAPIKeyComesFromEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("ai:\n  provider: openai\n  base_url: http://127.0.0.1:1/v1\n  model: test-model\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("OKGOBOT_AI_API_KEY", "sk-from-environment")

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.AI.APIKey != "sk-from-environment" {
		t.Fatalf("AI.APIKey = %q, want the value from OKGOBOT_AI_API_KEY", cfg.AI.APIKey)
	}
	if cfg.AI.Provider != "openai" || cfg.AI.BaseURL != "http://127.0.0.1:1/v1" {
		t.Fatalf("env override must not disturb file values: %+v", cfg.AI)
	}
}

func TestAIAPIKeyFromFileWinsWhenEnvUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("ai:\n  provider: openai\n  api_key: from-file\n  model: test-model\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.AI.APIKey != "from-file" {
		t.Fatalf("AI.APIKey = %q, want the file value", cfg.AI.APIKey)
	}
}
