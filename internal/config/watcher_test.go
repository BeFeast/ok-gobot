package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigWatcher(t *testing.T) {
	// Create temporary directory for test config
	tmpDir, err := os.MkdirTemp("", "config-watcher-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test config file
	configPath := filepath.Join(tmpDir, "config.yaml")
	initialContent := `telegram:
  token: "test-token-123"
ai:
  api_key: "test-key-123"
  model: "test-model"
  provider: "openrouter"
auth:
  mode: "open"
  admin_id: 12345
storage_path: "/tmp/test.db"
log_level: "info"
`

	if err := os.WriteFile(configPath, []byte(initialContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Track onChange calls
	callCount := 0
	var lastConfig *Config

	onChange := func(cfg *Config) {
		callCount++
		lastConfig = cfg
	}

	// Create watcher
	watcher, err := NewConfigWatcher(configPath, onChange)
	if err != nil {
		t.Fatalf("Failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	// Give watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Modify config file
	updatedContent := `telegram:
  token: "test-token-456"
ai:
  api_key: "test-key-456"
  model: "updated-model"
  provider: "openai"
auth:
  mode: "allowlist"
  admin_id: 67890
  allowed_users: [12345, 67890]
storage_path: "/tmp/test2.db"
log_level: "debug"
`

	if err := os.WriteFile(configPath, []byte(updatedContent), 0644); err != nil {
		t.Fatalf("Failed to update config file: %v", err)
	}

	// Wait for debounce and reload
	time.Sleep(1 * time.Second)

	// Check that onChange was called
	if callCount == 0 {
		t.Error("onChange was not called after config file update")
	}

	// Verify the updated config
	if lastConfig != nil {
		if lastConfig.Telegram.Token != "test-token-456" {
			t.Errorf("Expected token 'test-token-456', got '%s'", lastConfig.Telegram.Token)
		}

		if lastConfig.AI.Model != "updated-model" {
			t.Errorf("Expected model 'updated-model', got '%s'", lastConfig.AI.Model)
		}

		if lastConfig.Auth.Mode != "allowlist" {
			t.Errorf("Expected auth mode 'allowlist', got '%s'", lastConfig.Auth.Mode)
		}

		if lastConfig.LogLevel != "debug" {
			t.Errorf("Expected log level 'debug', got '%s'", lastConfig.LogLevel)
		}
	}
}

func TestConfigWatcherManualReload(t *testing.T) {
	// Create temporary directory for test config
	tmpDir, err := os.MkdirTemp("", "config-watcher-manual-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test config file
	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `telegram:
  token: "manual-token"
ai:
  api_key: "manual-key"
  model: "manual-model"
  provider: "openrouter"
auth:
  mode: "open"
storage_path: "/tmp/manual.db"
`

	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Track onChange calls
	callCount := 0

	onChange := func(cfg *Config) {
		callCount++
	}

	// Create watcher
	watcher, err := NewConfigWatcher(configPath, onChange)
	if err != nil {
		t.Fatalf("Failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	// Trigger manual reload
	if err := watcher.TriggerReload(); err != nil {
		t.Fatalf("Manual reload failed: %v", err)
	}

	// Check that onChange was called
	if callCount == 0 {
		t.Error("onChange was not called after manual reload")
	}
}

func TestConfigWatcherInvalidConfig(t *testing.T) {
	// Create temporary directory for test config
	tmpDir, err := os.MkdirTemp("", "config-watcher-invalid-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create test config file
	configPath := filepath.Join(tmpDir, "config.yaml")
	validContent := `telegram:
  token: "valid-token"
ai:
  api_key: "valid-key"
  model: "valid-model"
  provider: "openrouter"
auth:
  mode: "open"
storage_path: "/tmp/valid.db"
`

	if err := os.WriteFile(configPath, []byte(validContent), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Track onChange calls
	callCount := 0

	onChange := func(cfg *Config) {
		callCount++
	}

	// Create watcher
	watcher, err := NewConfigWatcher(configPath, onChange)
	if err != nil {
		t.Fatalf("Failed to create watcher: %v", err)
	}
	defer watcher.Stop()

	// Give watcher time to start
	time.Sleep(100 * time.Millisecond)

	// Write invalid config (missing required fields)
	invalidContent := `telegram:
  token: ""
ai:
  api_key: ""
  model: ""
`

	if err := os.WriteFile(configPath, []byte(invalidContent), 0644); err != nil {
		t.Fatalf("Failed to update config file: %v", err)
	}

	// Wait for debounce
	time.Sleep(1 * time.Second)

	// onChange should NOT be called for invalid config
	if callCount > 0 {
		t.Error("onChange should not be called for invalid config")
	}
}
