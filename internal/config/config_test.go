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
	if cfg.Runtime.SessionQueueLimit != 100 {
		t.Errorf("expected runtime.session_queue_limit=%d, got %d", 100, cfg.Runtime.SessionQueueLimit)
	}
	if cfg.Session.DMScope != "main" {
		t.Errorf("expected session.dm_scope=%q, got %q", "main", cfg.Session.DMScope)
	}
	if cfg.Memory.MetadataExtraction {
		t.Errorf("expected memory.metadata_extraction=false by default")
	}
	if cfg.Memory.MetadataModel != "haiku" {
		t.Errorf("expected memory.metadata_model=%q, got %q", "haiku", cfg.Memory.MetadataModel)
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
  session_queue_limit: 42
session:
  dm_scope: "per_user"
memory:
  metadata_extraction: true
  metadata_model: "claude-haiku-3.5"
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
	if cfg.Runtime.SessionQueueLimit != 42 {
		t.Errorf("expected runtime.session_queue_limit=%d, got %d", 42, cfg.Runtime.SessionQueueLimit)
	}
	if cfg.Session.DMScope != "per_user" {
		t.Errorf("expected session.dm_scope=%q, got %q", "per_user", cfg.Session.DMScope)
	}
	if !cfg.Memory.MetadataExtraction {
		t.Errorf("expected memory.metadata_extraction=true")
	}
	if cfg.Memory.MetadataModel != "claude-haiku-3.5" {
		t.Errorf("expected memory.metadata_model override, got %q", cfg.Memory.MetadataModel)
	}
}

func TestLoadFromDefaultMemoryMCPConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-memory-mcp-default-*")
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

	if cfg.Memory.MCP.Enabled {
		t.Fatalf("expected memory.mcp.enabled=false by default")
	}
	if cfg.Memory.MCP.Host != "127.0.0.1" {
		t.Fatalf("expected memory.mcp.host=%q, got %q", "127.0.0.1", cfg.Memory.MCP.Host)
	}
	if cfg.Memory.MCP.Port != 9233 {
		t.Fatalf("expected memory.mcp.port=%d, got %d", 9233, cfg.Memory.MCP.Port)
	}
	if cfg.Memory.MCP.Endpoint != "/mcp" {
		t.Fatalf("expected memory.mcp.endpoint=%q, got %q", "/mcp", cfg.Memory.MCP.Endpoint)
	}
	if cfg.Memory.MCP.AllowWrites {
		t.Fatalf("expected memory.mcp.allow_writes=false by default")
	}
}

func TestLoadFromExplicitMemoryMCPConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-memory-mcp-explicit-*")
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
memory:
  mcp:
    enabled: true
    host: "0.0.0.0"
    port: 4001
    endpoint: "/tooling/mcp"
    allow_writes: true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if !cfg.Memory.MCP.Enabled {
		t.Fatalf("expected memory.mcp.enabled=true")
	}
	if cfg.Memory.MCP.Host != "0.0.0.0" {
		t.Fatalf("expected memory.mcp.host=%q, got %q", "0.0.0.0", cfg.Memory.MCP.Host)
	}
	if cfg.Memory.MCP.Port != 4001 {
		t.Fatalf("expected memory.mcp.port=%d, got %d", 4001, cfg.Memory.MCP.Port)
	}
	if cfg.Memory.MCP.Endpoint != "/tooling/mcp" {
		t.Fatalf("expected memory.mcp.endpoint=%q, got %q", "/tooling/mcp", cfg.Memory.MCP.Endpoint)
	}
	if !cfg.Memory.MCP.AllowWrites {
		t.Fatalf("expected memory.mcp.allow_writes=true")
	}
}

func TestValidateRejectsInvalidRuntimeSessionQueueLimit(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-invalid-queue-*")
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
runtime:
  session_queue_limit: -1
storage_path: "/tmp/test.db"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for negative runtime.session_queue_limit")
	}
}

func TestValidateRejectsInvalidSessionDMScope(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-invalid-dm-scope-*")
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
session:
  dm_scope: "invalid"
storage_path: "/tmp/test.db"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for invalid session.dm_scope")
	}
}
