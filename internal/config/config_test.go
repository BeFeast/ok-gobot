package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromDefaultRuntimeMode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `telegram:
  token: "test-token"
ai:
  api_key: "test-key"
  model: "test-model"
  provider: "openrouter"
storage_path: "/tmp/test.db"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if cfg.Runtime.Mode != "hub" {
		t.Errorf("expected runtime.mode=%q, got %q", "hub", cfg.Runtime.Mode)
	}
}

func TestLoadFromExplicitRuntimeMode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-explicit-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `telegram:
  token: "test-token"
ai:
  api_key: "test-key"
  model: "test-model"
  provider: "openrouter"
storage_path: "/tmp/test.db"
runtime:
  mode: "legacy"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if cfg.Runtime.Mode != "legacy" {
		t.Errorf("expected runtime.mode=%q, got %q", "legacy", cfg.Runtime.Mode)
	}
}
