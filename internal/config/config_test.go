package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromDefaultRuntimeConfig(t *testing.T) {
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

	if cfg.Runtime.Mode != "" {
		t.Errorf("expected runtime.mode to remain empty by default, got %q", cfg.Runtime.Mode)
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

func TestLoadFromLegacyRuntimeModeCompatibility(t *testing.T) {
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
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected legacy runtime.mode compatibility to validate, got %v", err)
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

func TestLoadFromAgentCapabilities(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-capabilities-*")
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
agents:
  - name: "restricted"
    soul_path: "/tmp/soul"
    allowed_tools:
      - "file"
      - "grep"
    capabilities:
      shell: false
      network: false
      cron: false
      memory_write: true
      spawn: false
      file_write_scope: "read_only"
      filesystem_roots:
        - "/home/bot/workspace"
      network_allowlist:
        - "example.com"
  - name: "open"
    soul_path: "/tmp/soul"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if len(cfg.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(cfg.Agents))
	}

	// Restricted agent has capabilities set.
	restricted := cfg.Agents[0]
	if restricted.Capabilities == nil {
		t.Fatal("expected restricted agent to have capabilities")
	}
	if restricted.Capabilities.Shell == nil || *restricted.Capabilities.Shell != false {
		t.Error("expected shell=false")
	}
	if restricted.Capabilities.Network == nil || *restricted.Capabilities.Network != false {
		t.Error("expected network=false")
	}
	if restricted.Capabilities.Cron == nil || *restricted.Capabilities.Cron != false {
		t.Error("expected cron=false")
	}
	if restricted.Capabilities.MemoryWrite == nil || *restricted.Capabilities.MemoryWrite != true {
		t.Error("expected memory_write=true")
	}
	if restricted.Capabilities.Spawn == nil || *restricted.Capabilities.Spawn != false {
		t.Error("expected spawn=false")
	}
	if restricted.Capabilities.FileWriteScope != "read_only" {
		t.Errorf("expected file_write_scope=read_only, got %q", restricted.Capabilities.FileWriteScope)
	}
	if len(restricted.Capabilities.FilesystemRoots) != 1 || restricted.Capabilities.FilesystemRoots[0] != "/home/bot/workspace" {
		t.Errorf("unexpected filesystem_roots: %v", restricted.Capabilities.FilesystemRoots)
	}
	if len(restricted.Capabilities.NetworkAllowlist) != 1 || restricted.Capabilities.NetworkAllowlist[0] != "example.com" {
		t.Errorf("unexpected network_allowlist: %v", restricted.Capabilities.NetworkAllowlist)
	}

	// Open agent has no capabilities set.
	open := cfg.Agents[1]
	if open.Capabilities != nil {
		t.Error("expected open agent to have nil capabilities")
	}

	// Validation should pass.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected validation to pass, got %v", err)
	}
}

func TestValidateRejectsInvalidFileWriteScope(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-fws-*")
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
storage_path: "/tmp/test.db"
agents:
  - name: "bad"
    soul_path: "/tmp/soul"
    capabilities:
      file_write_scope: "invalid"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for invalid file_write_scope")
	}
}

func TestLoadFromAgentWithoutCapabilitiesBackwardCompat(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-nocp-*")
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
storage_path: "/tmp/test.db"
agents:
  - name: "legacy"
    soul_path: "/tmp/soul"
    allowed_tools:
      - "file"
      - "local"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if len(cfg.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(cfg.Agents))
	}
	agent := cfg.Agents[0]
	if agent.Capabilities != nil {
		t.Error("expected nil capabilities for legacy agent config")
	}
	if len(agent.AllowedTools) != 2 {
		t.Errorf("expected 2 allowed_tools, got %d", len(agent.AllowedTools))
	}
}
