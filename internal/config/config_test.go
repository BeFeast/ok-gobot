package config

import (
	"os"
	"path/filepath"
	"strings"
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
	if cfg.Memory.Backend != "builtin" {
		t.Errorf("expected memory.backend=%q, got %q", "builtin", cfg.Memory.Backend)
	}
	if cfg.Memory.QMD.BinaryPath != "qmd" {
		t.Errorf("expected memory.qmd.binary_path=%q, got %q", "qmd", cfg.Memory.QMD.BinaryPath)
	}
	if cfg.Memory.QMD.SearchMode != "search" {
		t.Errorf("expected memory.qmd.search_mode=%q, got %q", "search", cfg.Memory.QMD.SearchMode)
	}
	if cfg.Maestro.ReadyLabel != "ready" {
		t.Errorf("expected maestro.ready_label=%q, got %q", "ready", cfg.Maestro.ReadyLabel)
	}
	if cfg.Maestro.Limit != 50 {
		t.Errorf("expected maestro.limit=%d, got %d", 50, cfg.Maestro.Limit)
	}
	if len(cfg.Maestro.HardExcludeLabels) != 7 {
		t.Errorf("expected maestro hard excludes, got %#v", cfg.Maestro.HardExcludeLabels)
	}
	if cfg.Obsidian.VaultDir != "" {
		t.Errorf("expected no default Obsidian vault path, got %q", cfg.Obsidian.VaultDir)
	}
	if cfg.VideoSummary.ScribeURL != "" {
		t.Errorf("expected no default Scribe URL, got %q", cfg.VideoSummary.ScribeURL)
	}
	if cfg.YouTubeKaraoke.BaseURL != "" {
		t.Errorf("expected no default Karaoke URL, got %q", cfg.YouTubeKaraoke.BaseURL)
	}
	if cfg.YouTubeKaraoke.PollInterval != "5s" {
		t.Errorf("expected youtube_karaoke.poll_interval=%q, got %q", "5s", cfg.YouTubeKaraoke.PollInterval)
	}
	if !filepath.IsAbs(cfg.YouTubeKaraoke.OutputDir) {
		t.Errorf("expected expanded youtube_karaoke.output_dir, got %q", cfg.YouTubeKaraoke.OutputDir)
	}
	if cfg.AI.ChatGPT.BinaryPath != "codex" {
		t.Errorf("expected ai.chatgpt.binary_path=%q, got %q", "codex", cfg.AI.ChatGPT.BinaryPath)
	}
}

func TestLoadAndValidateChatGPTCodexAuthWithoutAPIKey(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `telegram:
  token: "test-token"
ai:
  provider: "chatgpt"
  model: "gpt-5.4"
  chatgpt:
    auth_file: "~/.codex-test/auth.json"
    codex_home: "~/.codex-test"
    binary_path: "codex-custom"
storage_path: "~/.ok-gobot/test.db"
`
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if cfg.AI.APIKey != "" {
		t.Fatalf("APIKey = %q, want empty", cfg.AI.APIKey)
	}
	if !filepath.IsAbs(cfg.AI.ChatGPT.AuthFile) || !filepath.IsAbs(cfg.AI.ChatGPT.CodexHome) {
		t.Fatalf("ChatGPT paths were not expanded: %#v", cfg.AI.ChatGPT)
	}
	if cfg.AI.ChatGPT.BinaryPath != "codex-custom" {
		t.Fatalf("BinaryPath = %q", cfg.AI.ChatGPT.BinaryPath)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() rejected Codex auth: %v", err)
	}
}

func TestSavePersistsChatGPTCodexAuthSettings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{
		ConfigPath: configPath,
		AI: AIConfig{
			Provider: "chatgpt",
			Model:    "gpt-5.4",
			ChatGPT: ChatGPTConfig{
				AuthFile:   "/secure/codex/auth.json",
				CodexHome:  "/secure/codex",
				BinaryPath: "/usr/local/bin/codex",
			},
		},
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	if loaded.AI.ChatGPT != cfg.AI.ChatGPT {
		t.Fatalf("ChatGPT settings = %#v, want %#v", loaded.AI.ChatGPT, cfg.AI.ChatGPT)
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
  backend: auto
  extra_paths:
    - name: notes
      path: "~/ok-memory"
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
	if cfg.Memory.Backend != "auto" {
		t.Errorf("expected memory.backend=auto, got %q", cfg.Memory.Backend)
	}
	if len(cfg.Memory.ExtraPaths) != 1 || !filepath.IsAbs(cfg.Memory.ExtraPaths[0].Path) {
		t.Errorf("expected expanded memory.extra_paths, got %#v", cfg.Memory.ExtraPaths)
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

func TestLoadFromExplicitQMDMemoryConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-memory-qmd-explicit-*")
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
  enabled: true
  backend: "qmd"
  qmd:
    binary_path: "/usr/local/bin/qmd"
    index: "work"
    index_path: "/tmp/qmd.sqlite"
    search_mode: "query"
    timeout: "5s"
    fallback_cooldown: "30s"
    collections:
      workspace: "ok-workspace"
      daily_notes: "ok-daily"
      session_transcripts: "ok-sessions"
      extra_paths:
        - "docs"
        - "notes"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}

	if cfg.Memory.Backend != "qmd" {
		t.Fatalf("expected qmd backend, got %q", cfg.Memory.Backend)
	}
	if cfg.Memory.QMD.BinaryPath != "/usr/local/bin/qmd" || cfg.Memory.QMD.Index != "work" || cfg.Memory.QMD.IndexPath != "/tmp/qmd.sqlite" {
		t.Fatalf("unexpected qmd paths: %+v", cfg.Memory.QMD)
	}
	if cfg.Memory.QMD.SearchMode != "query" || cfg.Memory.QMD.Timeout != "5s" || cfg.Memory.QMD.FallbackCooldown != "30s" {
		t.Fatalf("unexpected qmd timing/search config: %+v", cfg.Memory.QMD)
	}
	if cfg.Memory.QMD.Collections.Workspace != "ok-workspace" {
		t.Fatalf("workspace collection mismatch: %+v", cfg.Memory.QMD.Collections)
	}
	if len(cfg.Memory.QMD.Collections.ExtraPaths) != 2 || cfg.Memory.QMD.Collections.ExtraPaths[1] != "notes" {
		t.Fatalf("extra paths mismatch: %+v", cfg.Memory.QMD.Collections.ExtraPaths)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected qmd config to validate, got %v", err)
	}
}

func TestValidateRejectsInvalidQMDSearchMode(t *testing.T) {
	cfg := &Config{
		Telegram:    TelegramConfig{Token: "test-token"},
		AI:          AIConfig{APIKey: "test-key", Model: "test-model"},
		StoragePath: "/tmp/test.db",
		Maestro:     MaestroConfig{ReadyLabel: "ready"},
		Memory: MemoryConfig{
			Backend: "qmd",
			QMD:     MemoryQMDConfig{SearchMode: "bad", Timeout: "1s", FallbackCooldown: "1m"},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid qmd search mode error")
	}
}

func TestLoadFromExplicitYouTubeKaraokeConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-youtube-karaoke-*")
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
youtube_karaoke:
  base_url: "https://karaoke.example"
  api_token: "karaoke-test-token"
  output_dir: "~/Karaoke"
  poll_interval: "3s"
  timeout: "45m"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom failed: %v", err)
	}
	if !filepath.IsAbs(cfg.YouTubeKaraoke.OutputDir) || !strings.HasSuffix(cfg.YouTubeKaraoke.OutputDir, "Karaoke") {
		t.Fatalf("youtube_karaoke.output_dir was not expanded: %q", cfg.YouTubeKaraoke.OutputDir)
	}
	if cfg.YouTubeKaraoke.BaseURL != "https://karaoke.example" || cfg.YouTubeKaraoke.APIToken != "karaoke-test-token" || cfg.YouTubeKaraoke.PollInterval != "3s" || cfg.YouTubeKaraoke.Timeout != "45m" {
		t.Fatalf("unexpected youtube_karaoke config: %+v", cfg.YouTubeKaraoke)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected youtube_karaoke config to validate, got %v", err)
	}
}

func TestLoadFromObsidianEnvironmentAndLegacyAlias(t *testing.T) {
	t.Setenv("OKGOBOT_OBSIDIAN_VAULT_DIR", "~/ConfiguredVault")
	t.Setenv("OKGOBOT_VIDEO_SUMMARY_API_TOKEN", "scribe-env-token")

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := `telegram:
  token: "test-token"
ai:
  api_key: "test-key"
  model: "test-model"
  provider: "openrouter"
storage_path: "/tmp/test.db"
video_summary:
  scribe_url: "https://scribe.example"
  vault_dir: "~/LegacyVault"
`
	if err := os.WriteFile(configPath, []byte(content), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if !filepath.IsAbs(cfg.Obsidian.VaultDir) || !strings.HasSuffix(cfg.Obsidian.VaultDir, "ConfiguredVault") {
		t.Fatalf("Obsidian.VaultDir = %q", cfg.Obsidian.VaultDir)
	}
	if cfg.VideoSummary.VaultDir != cfg.Obsidian.VaultDir {
		t.Fatalf("video summary vault = %q, want %q", cfg.VideoSummary.VaultDir, cfg.Obsidian.VaultDir)
	}
	if cfg.VideoSummary.APIToken != "scribe-env-token" {
		t.Fatalf("VideoSummary.APIToken did not load from environment")
	}
}

func TestSavePersistsObsidianAndMediaServiceConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &Config{
		ConfigPath:  configPath,
		Telegram:    TelegramConfig{Token: "test-token"},
		AI:          AIConfig{APIKey: "test-key", Model: "test-model", Provider: "openrouter"},
		Auth:        AuthConfig{Mode: "open"},
		StoragePath: "/tmp/test.db",
		Obsidian:    ObsidianConfig{VaultDir: "/vault"},
		VideoSummary: VideoSummaryConfig{
			ScribeURL: "https://scribe.example", APIToken: "scribe-token", SummaryPrompt: "Detailed",
			PollInterval: "5s", Timeout: "2h", VaultDir: "/legacy-should-not-be-written",
		},
		YouTubeKaraoke: YouTubeKaraokeConfig{
			BaseURL: "https://karaoke.example", APIToken: "karaoke-token", OutputDir: "/artifacts",
			PollInterval: "5s", Timeout: "2h",
		},
		Maestro: MaestroConfig{ReadyLabel: "ready"},
	}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	text := string(data)
	for _, want := range []string{"vault_dir: /vault", "scribe_url: https://scribe.example", "base_url: https://karaoke.example", "summary_prompt: Detailed"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved config missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "legacy-should-not-be-written") {
		t.Fatalf("saved config retained deprecated video_summary.vault_dir:\n%s", text)
	}
}

func TestValidateRejectsBlankMaestroReadyLabel(t *testing.T) {
	cfg := &Config{
		Telegram:    TelegramConfig{Token: "test-token"},
		AI:          AIConfig{APIKey: "test-key", Model: "test-model"},
		Auth:        AuthConfig{Mode: "open"},
		StoragePath: "/tmp/test.db",
		Maestro:     MaestroConfig{ReadyLabel: " "},
	}

	err := cfg.Validate()
	if err == nil || err.Error() != "invalid maestro.ready_label: must not be empty" {
		t.Fatalf("Validate error = %v, want invalid maestro.ready_label", err)
	}
}

func TestSavePersistsMaestroConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := &Config{
		ConfigPath:  configPath,
		Telegram:    TelegramConfig{Token: "test-token"},
		AI:          AIConfig{APIKey: "test-key", Model: "test-model", Provider: "openrouter"},
		Auth:        AuthConfig{Mode: "open"},
		StoragePath: "/tmp/test.db",
		Maestro: MaestroConfig{
			Repo:              "BeFeast/ok-gobot",
			ReadyLabel:        "ready-for-maestro",
			HardExcludeLabels: []string{"blocked", "meta"},
			Limit:             17,
		},
	}

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if loaded.Maestro.Repo != cfg.Maestro.Repo || loaded.Maestro.ReadyLabel != cfg.Maestro.ReadyLabel || loaded.Maestro.Limit != cfg.Maestro.Limit {
		t.Fatalf("loaded maestro scalar config = %+v, want %+v", loaded.Maestro, cfg.Maestro)
	}
	if len(loaded.Maestro.HardExcludeLabels) != 2 || loaded.Maestro.HardExcludeLabels[0] != "blocked" || loaded.Maestro.HardExcludeLabels[1] != "meta" {
		t.Fatalf("loaded hard excludes = %#v", loaded.Maestro.HardExcludeLabels)
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

func TestLoadFromMemoryModeDefault(t *testing.T) {
	tmpDir := t.TempDir()
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
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Memory.Mode != MemoryModeEager {
		t.Errorf("memory.mode default = %q, want %q", cfg.Memory.Mode, MemoryModeEager)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLoadFromMemoryModeRetrievalFirst(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `telegram:
  token: "test-token"
ai:
  api_key: "test-key"
  model: "test-model"
  provider: "openrouter"
storage_path: "/tmp/test.db"
memory:
  mode: "retrieval_first"
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if cfg.Memory.Mode != MemoryModeRetrievalFirst {
		t.Errorf("memory.mode = %q, want %q", cfg.Memory.Mode, MemoryModeRetrievalFirst)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateRejectsUnknownMemoryMode(t *testing.T) {
	cfg := &Config{
		Telegram:    TelegramConfig{Token: "t"},
		AI:          AIConfig{APIKey: "k", Model: "m", Provider: "openrouter"},
		Auth:        AuthConfig{Mode: "open"},
		Memory:      MemoryConfig{Mode: "wholly-invented"},
		StoragePath: "/tmp/x",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("Validate must reject unknown memory.mode")
	}
}

func TestValidateAcceptsCaseVariantMemoryMode(t *testing.T) {
	for _, mode := range []string{"EAGER", "Eager", "Retrieval_First", "  startup_recent "} {
		cfg := &Config{
			Telegram:    TelegramConfig{Token: "t"},
			AI:          AIConfig{APIKey: "k", Model: "m", Provider: "openrouter"},
			Auth:        AuthConfig{Mode: "open"},
			Memory:      MemoryConfig{Mode: mode},
			Maestro:     MaestroConfig{ReadyLabel: "ready"},
			StoragePath: "/tmp/x",
		}
		if err := cfg.Validate(); err != nil {
			t.Errorf("Validate(memory.mode=%q) unexpected error: %v", mode, err)
		}
	}
}

func TestNormalizeMemoryMode(t *testing.T) {
	cases := map[string]string{
		"":                   MemoryModeEager,
		"eager":              MemoryModeEager,
		"EAGER":              MemoryModeEager,
		"  retrieval_first ": MemoryModeRetrievalFirst,
		"startup_recent":     MemoryModeStartupRecent,
	}
	for in, want := range cases {
		if got := NormalizeMemoryMode(in); got != want {
			t.Errorf("NormalizeMemoryMode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeMemoryMode("???"); got != "???" {
		t.Errorf("unknown values should be returned verbatim so Validate can reject them, got %q", got)
	}
}

func TestLoadFromSkillsTrustWorkspaceScripts(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `telegram:
  token: "test-token"
ai:
  api_key: "test-key"
  model: "test-model"
  provider: "openrouter"
storage_path: "/tmp/test.db"
skills:
  trust_workspace_scripts: true
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if !cfg.TrustWorkspaceScripts() {
		t.Fatal("TrustWorkspaceScripts() = false, want true")
	}
}

func TestTrustWorkspaceScriptsEnvOverride(t *testing.T) {
	t.Setenv("OKGOBOT_SKILLS_TRUST_WORKSPACE_SCRIPTS", "true")
	cfg := &Config{}
	if !cfg.TrustWorkspaceScripts() {
		t.Fatal("env should enable trusted workspace scripts")
	}

	t.Setenv("OKGOBOT_SKILLS_TRUST_WORKSPACE_SCRIPTS", "false")
	cfg.Skills.TrustWorkspaceScripts = true
	if cfg.TrustWorkspaceScripts() {
		t.Fatal("env=false should override config=true")
	}

	t.Setenv("OKGOBOT_SKILLS_TRUST_WORKSPACE_SCRIPTS", "enable")
	if !cfg.TrustWorkspaceScripts() {
		t.Fatal("invalid env should fall back to config=true")
	}
}
