package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"

	"ok-gobot/internal/bootstrap"
)

// DefaultModelAliases provides shorthand names for popular models.
// Users can type `/model sonnet` instead of the full model identifier.
var DefaultModelAliases = map[string]string{
	"sonnet":   "claude-sonnet-4-5-20250929",
	"opus":     "claude-opus-4-5-20251101",
	"haiku":    "claude-haiku-3-5-20241022",
	"gpt4":     "openai/gpt-4o",
	"gpt4m":    "openai/gpt-4o-mini",
	"kimi":     "moonshotai/kimi-k2.5",
	"gemini":   "google/gemini-2.5-pro",
	"flash":    "google/gemini-2.5-flash",
	"deepseek": "deepseek/deepseek-chat-v3-0324",
	"glm":      "glm-5",
	"minimax":  "minimax-m2.5",
}

// ControlConfig holds configuration for the loopback WebSocket control server.
type ControlConfig struct {
	Enabled                   bool   `mapstructure:"enabled"`
	Port                      int    `mapstructure:"port"`
	Token                     string `mapstructure:"token"`
	AllowLoopbackWithoutToken bool   `mapstructure:"allow_loopback_without_token"`
}

// RuntimeConfig holds runtime mailbox configuration.
type RuntimeConfig struct {
	// Mode is a legacy compatibility knob retained so older configs still decode.
	// It is ignored: the active architecture contract is the chat/jobs mailbox runtime.
	// Supported legacy values remain "", "hub", and "legacy" while removal is pending.
	Mode string `mapstructure:"mode"`
	// SessionQueueLimit is the per-session queue capacity for chat/jobs mailbox execution.
	// 0 falls back to runtime defaults where applicable for the active runtime path.
	SessionQueueLimit int `mapstructure:"session_queue_limit"`
	// CostTiers defines global execution settings per cost tier.
	// Keys are tier names: "premium", "standard", "cheap", "local".
	CostTiers map[string]CostTierEntry `mapstructure:"cost_tiers"`
	// Roles defines named role policies that map work to cost tiers.
	Roles []RolePolicyEntry `mapstructure:"roles"`
}

// CostTierEntry describes the execution settings for one cost tier in configuration.
type CostTierEntry struct {
	Model        string `mapstructure:"model"`
	Provider     string `mapstructure:"provider"`
	BaseURL      string `mapstructure:"base_url"`
	Thinking     string `mapstructure:"thinking"`
	MaxToolCalls int    `mapstructure:"max_tool_calls"`
	MaxDuration  string `mapstructure:"max_duration"` // parseable duration, e.g. "5m"
}

// RolePolicyEntry describes how a named role routes work across cost tiers.
type RolePolicyEntry struct {
	Name        string                   `mapstructure:"name"`
	DefaultTier string                   `mapstructure:"default_tier"`
	Tiers       map[string]CostTierEntry `mapstructure:"tiers"`
}

// BrowserConfig holds browser automation settings.
type BrowserConfig struct {
	ChromePath  string `mapstructure:"chrome_path"`  // explicit path to Chrome/Chromium binary
	ProfilePath string `mapstructure:"profile_path"` // user data directory for browser profiles
	DebugURL    string `mapstructure:"debug_url"`    // connect to existing browser (e.g. http://127.0.0.1:9222)
}

// SessionConfig holds session-key derivation behavior.
type SessionConfig struct {
	// DMScope controls how DM session keys are created:
	// "main" (default): shared main session
	// "per_user": one session per Telegram user
	DMScope string `mapstructure:"dm_scope"`
}

// Config holds all application configuration
type Config struct {
	ConfigPath   string            `mapstructure:"-"`
	Telegram     TelegramConfig    `mapstructure:"telegram"`
	AI           AIConfig          `mapstructure:"ai"`
	Auth         AuthConfig        `mapstructure:"auth"`
	API          APIConfig         `mapstructure:"api"`
	Control      ControlConfig     `mapstructure:"control"`
	Browser      BrowserConfig     `mapstructure:"browser"`
	Runtime      RuntimeConfig     `mapstructure:"runtime"`
	Session      SessionConfig     `mapstructure:"session"`
	Groups       GroupsConfig      `mapstructure:"groups"`
	TTS          TTSConfig         `mapstructure:"tts"`
	Memory       MemoryConfig      `mapstructure:"memory"`
	Agents       []AgentConfig     `mapstructure:"agents"`
	Models       []string          `mapstructure:"models"` // list of models for TUI/web picker
	ModelAliases map[string]string `mapstructure:"model_aliases"`
	Contacts     map[string]int64  `mapstructure:"contacts"` // alias -> chatID for message tool allowlist
	StoragePath  string            `mapstructure:"storage_path"`
	LogLevel     string            `mapstructure:"log_level"`
	SoulPath     string            `mapstructure:"soul_path"` // Path to agent personality files (deprecated, use agents)
}

// TelegramConfig holds Telegram bot configuration
type TelegramConfig struct {
	Token   string `mapstructure:"token"`
	Webhook string `mapstructure:"webhook"`
}

// AIConfig holds AI provider configuration.
// Supports: openrouter, openai, anthropic, droid, chatgpt (openai-codex), or custom OpenAI-compatible APIs.
type AIConfig struct {
	Provider        string      `mapstructure:"provider"` // "openrouter", "openai", "anthropic", "droid", "chatgpt", "openai-codex", "custom"
	APIKey          string      `mapstructure:"api_key"`
	Model           string      `mapstructure:"model"`
	BaseURL         string      `mapstructure:"base_url"`         // For custom providers
	FallbackModels  []string    `mapstructure:"fallback_models"`  // Models to try if primary fails
	DefaultThinking string      `mapstructure:"default_thinking"` // Default thinking level: "off", "low", "medium", "high", "adaptive"
	Droid           DroidConfig `mapstructure:"droid"`            // Droid-specific settings (provider=droid)
}

// DroidConfig holds configuration for the factory.ai droid provider.
type DroidConfig struct {
	BinaryPath string `mapstructure:"binary_path"` // Path to droid binary (default: "droid")
	AutoLevel  string `mapstructure:"auto_level"`  // Autonomy level: "", "low", "medium", "high"
	WorkDir    string `mapstructure:"work_dir"`    // Working directory for droid execution
}

// AuthConfig holds authorization configuration
type AuthConfig struct {
	Mode         string  `mapstructure:"mode"`          // "open", "allowlist", "pairing"
	AllowedUsers []int64 `mapstructure:"allowed_users"` // List of allowed Telegram user IDs
	AdminID      int64   `mapstructure:"admin_id"`      // Admin user ID who can manage auth
}

// APIConfig holds HTTP API configuration
type APIConfig struct {
	Enabled     bool   `mapstructure:"enabled"`      // Enable HTTP API server
	Port        int    `mapstructure:"port"`         // API server port
	BindAddr    string `mapstructure:"bind_addr"`    // Bind address (default "127.0.0.1")
	APIKey      string `mapstructure:"api_key"`      // Required API key for authentication
	WebhookChat int64  `mapstructure:"webhook_chat"` // Default chat ID for webhook messages
}

// GroupsConfig holds group chat configuration
type GroupsConfig struct {
	DefaultMode string `mapstructure:"default_mode"` // "active" or "standby"
}

// TTSConfig holds text-to-speech configuration
type TTSConfig struct {
	Provider     string `mapstructure:"provider"`      // "openai" or "edge"
	DefaultVoice string `mapstructure:"default_voice"` // Provider-specific default voice
}

// MemoryConfig holds semantic memory configuration
type MemoryConfig struct {
	Enabled            bool            `mapstructure:"enabled"`             // Enable semantic memory
	EmbeddingsBaseURL  string          `mapstructure:"embeddings_base_url"` // API base URL for embeddings
	EmbeddingsAPIKey   string          `mapstructure:"embeddings_api_key"`  // API key for embeddings (can reuse ai.api_key)
	EmbeddingsModel    string          `mapstructure:"embeddings_model"`    // Embeddings model to use
	MetadataExtraction bool            `mapstructure:"metadata_extraction"` // Extract structured metadata while indexing memories
	MetadataModel      string          `mapstructure:"metadata_model"`      // LLM model used for metadata extraction
	MCP                MemoryMCPConfig `mapstructure:"mcp"`                 // Optional MCP server exposing memory tools
}

// MemoryMCPConfig holds memory MCP server configuration
type MemoryMCPConfig struct {
	Enabled     bool   `mapstructure:"enabled"`      // Enable memory MCP server
	Host        string `mapstructure:"host"`         // Bind host (default loopback only)
	Port        int    `mapstructure:"port"`         // MCP server port
	Endpoint    string `mapstructure:"endpoint"`     // MCP endpoint path
	AllowWrites bool   `mapstructure:"allow_writes"` // Allow write tools such as memory_capture
}

// AgentConfig holds configuration for a single agent
type AgentConfig struct {
	Name         string   `mapstructure:"name"`
	SoulPath     string   `mapstructure:"soul_path"`
	Model        string   `mapstructure:"model"`         // Empty = use global ai.model
	AllowedTools []string `mapstructure:"allowed_tools"` // Empty = all tools allowed
}

// OpenAIConfig holds OpenAI API configuration (legacy, use AIConfig)
type OpenAIConfig struct {
	APIKey string `mapstructure:"api_key"`
	Model  string `mapstructure:"model"`
}

// Load reads configuration from file and environment
func Load() (*Config, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("log_level", "info")
	v.SetDefault("storage_path", "~/.ok-gobot/ok-gobot.db")
	v.SetDefault("soul_path", bootstrap.DefaultPath) // Default to visible directory
	v.SetDefault("ai.provider", "openrouter")
	v.SetDefault("ai.model", "moonshotai/kimi-k2.5")
	v.SetDefault("ai.droid.binary_path", "droid")
	v.SetDefault("ai.droid.auto_level", "")
	v.SetDefault("ai.droid.work_dir", "")
	v.SetDefault("auth.mode", "open")
	v.SetDefault("auth.allowed_users", []int64{})
	v.SetDefault("auth.admin_id", int64(0))
	v.SetDefault("groups.default_mode", "standby")
	v.SetDefault("api.enabled", false)
	v.SetDefault("api.port", 8080)
	v.SetDefault("api.bind_addr", "127.0.0.1")
	v.SetDefault("api.api_key", "")
	v.SetDefault("api.webhook_chat", int64(0))
	v.SetDefault("tts.provider", "openai")
	v.SetDefault("tts.default_voice", "")
	v.SetDefault("memory.enabled", false)
	v.SetDefault("memory.embeddings_base_url", "https://api.openai.com/v1")
	v.SetDefault("memory.embeddings_api_key", "")
	v.SetDefault("memory.embeddings_model", "text-embedding-3-small")
	v.SetDefault("memory.metadata_extraction", false)
	v.SetDefault("memory.metadata_model", "haiku")
	v.SetDefault("memory.mcp.enabled", false)
	v.SetDefault("memory.mcp.host", "127.0.0.1")
	v.SetDefault("memory.mcp.port", 9233)
	v.SetDefault("memory.mcp.endpoint", "/mcp")
	v.SetDefault("memory.mcp.allow_writes", false)
	v.SetDefault("control.enabled", false)
	v.SetDefault("control.port", 8787)
	v.SetDefault("control.token", "")
	v.SetDefault("control.allow_loopback_without_token", true)
	v.SetDefault("runtime.session_queue_limit", 100)
	v.SetDefault("session.dm_scope", "main")

	// Environment variable prefix
	v.SetEnvPrefix("OKGOBOT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Find config directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	configDir := filepath.Join(homeDir, ".ok-gobot")
	configFile := filepath.Join(configDir, "config")

	// Check if config exists
	if _, err := os.Stat(configFile + ".yaml"); err == nil {
		v.SetConfigFile(configFile + ".yaml")
	} else if _, err := os.Stat(configFile + ".yml"); err == nil {
		v.SetConfigFile(configFile + ".yml")
	} else if _, err := os.Stat(configFile + ".json"); err == nil {
		v.SetConfigFile(configFile + ".json")
	}

	// Read config if it exists
	if v.ConfigFileUsed() != "" {
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
	}

	// Unmarshal to struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Expand paths
	cfg.StoragePath = expandPath(cfg.StoragePath)
	cfg.SoulPath = expandPath(cfg.SoulPath)
	cfg.ConfigPath = v.ConfigFileUsed()

	// Migrate legacy openai config to ai config
	var legacyOpenAI OpenAIConfig
	if err := v.UnmarshalKey("openai", &legacyOpenAI); err == nil {
		if legacyOpenAI.APIKey != "" && cfg.AI.APIKey == "" {
			cfg.AI.APIKey = legacyOpenAI.APIKey
			cfg.AI.Provider = "openai"
			if legacyOpenAI.Model != "" {
				cfg.AI.Model = legacyOpenAI.Model
			}
		}
	}

	return &cfg, nil
}

// LoadFrom reads configuration from a specific file path
func LoadFrom(configPath string) (*Config, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("log_level", "info")
	v.SetDefault("storage_path", "~/.ok-gobot/ok-gobot.db")
	v.SetDefault("soul_path", bootstrap.DefaultPath)
	v.SetDefault("ai.provider", "openrouter")
	v.SetDefault("ai.model", "moonshotai/kimi-k2.5")
	v.SetDefault("ai.droid.binary_path", "droid")
	v.SetDefault("ai.droid.auto_level", "")
	v.SetDefault("ai.droid.work_dir", "")
	v.SetDefault("auth.mode", "open")
	v.SetDefault("auth.allowed_users", []int64{})
	v.SetDefault("auth.admin_id", int64(0))
	v.SetDefault("groups.default_mode", "standby")
	v.SetDefault("api.enabled", false)
	v.SetDefault("api.port", 8080)
	v.SetDefault("api.bind_addr", "127.0.0.1")
	v.SetDefault("api.api_key", "")
	v.SetDefault("api.webhook_chat", int64(0))
	v.SetDefault("tts.provider", "openai")
	v.SetDefault("tts.default_voice", "")
	v.SetDefault("memory.enabled", false)
	v.SetDefault("memory.embeddings_base_url", "https://api.openai.com/v1")
	v.SetDefault("memory.embeddings_api_key", "")
	v.SetDefault("memory.embeddings_model", "text-embedding-3-small")
	v.SetDefault("memory.metadata_extraction", false)
	v.SetDefault("memory.metadata_model", "haiku")
	v.SetDefault("memory.mcp.enabled", false)
	v.SetDefault("memory.mcp.host", "127.0.0.1")
	v.SetDefault("memory.mcp.port", 9233)
	v.SetDefault("memory.mcp.endpoint", "/mcp")
	v.SetDefault("memory.mcp.allow_writes", false)
	v.SetDefault("control.enabled", false)
	v.SetDefault("control.port", 8787)
	v.SetDefault("control.token", "")
	v.SetDefault("control.allow_loopback_without_token", true)
	v.SetDefault("runtime.session_queue_limit", 100)
	v.SetDefault("session.dm_scope", "main")

	// Environment variable prefix
	v.SetEnvPrefix("OKGOBOT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Load from specific config file
	v.SetConfigFile(configPath)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	// Unmarshal to struct
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Expand paths
	cfg.StoragePath = expandPath(cfg.StoragePath)
	cfg.SoulPath = expandPath(cfg.SoulPath)
	cfg.ConfigPath = configPath

	// Migrate legacy openai config to ai config
	var legacyOpenAI OpenAIConfig
	if err := v.UnmarshalKey("openai", &legacyOpenAI); err == nil {
		if legacyOpenAI.APIKey != "" && cfg.AI.APIKey == "" {
			cfg.AI.APIKey = legacyOpenAI.APIKey
			cfg.AI.Provider = "openai"
			if legacyOpenAI.Model != "" {
				cfg.AI.Model = legacyOpenAI.Model
			}
		}
	}

	return &cfg, nil
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Check Telegram token
	if c.Telegram.Token == "" {
		return fmt.Errorf("telegram.token is required")
	}

	// Check AI configuration
	if c.AI.APIKey == "" && c.AI.Provider != "droid" {
		return fmt.Errorf("ai.api_key is required")
	}

	if c.AI.Model == "" {
		return fmt.Errorf("ai.model is required")
	}

	// Validate auth mode
	validModes := map[string]bool{"open": true, "allowlist": true, "pairing": true}
	if !validModes[c.Auth.Mode] {
		return fmt.Errorf("invalid auth.mode: %s (must be 'open', 'allowlist', or 'pairing')", c.Auth.Mode)
	}

	// Validate legacy runtime.mode compatibility.
	validRuntimeModes := map[string]bool{"": true, "hub": true, "legacy": true}
	if !validRuntimeModes[c.Runtime.Mode] {
		return fmt.Errorf("invalid runtime.mode: %q (legacy compatibility only; allowed: '', 'hub', 'legacy')", c.Runtime.Mode)
	}
	if c.Runtime.SessionQueueLimit < 0 {
		return fmt.Errorf("invalid runtime.session_queue_limit: %d (must be >= 0)", c.Runtime.SessionQueueLimit)
	}
	validCostTiers := map[string]bool{
		"premium": true, "standard": true, "cheap": true, "local": true,
	}
	for name := range c.Runtime.CostTiers {
		if !validCostTiers[name] {
			return fmt.Errorf("invalid runtime.cost_tiers key: %q (allowed: premium, standard, cheap, local)", name)
		}
	}
	for _, role := range c.Runtime.Roles {
		if role.Name == "" {
			return fmt.Errorf("runtime.roles: each role must have a name")
		}
		if role.DefaultTier != "" && !validCostTiers[role.DefaultTier] {
			return fmt.Errorf("runtime.roles[%s].default_tier: invalid tier %q", role.Name, role.DefaultTier)
		}
		for tierName := range role.Tiers {
			if !validCostTiers[tierName] {
				return fmt.Errorf("runtime.roles[%s].tiers: invalid tier %q", role.Name, tierName)
			}
		}
	}

	// Validate session DM scope
	validDMScope := map[string]bool{"main": true, "per_user": true}
	if c.Session.DMScope != "" && !validDMScope[c.Session.DMScope] {
		return fmt.Errorf("invalid session.dm_scope: %s (must be 'main' or 'per_user')", c.Session.DMScope)
	}

	// Validate TTS provider
	if c.TTS.Provider != "" {
		validTTSProviders := map[string]bool{"openai": true, "edge": true}
		if !validTTSProviders[c.TTS.Provider] {
			return fmt.Errorf("invalid tts.provider: %s (must be 'openai' or 'edge')", c.TTS.Provider)
		}
	}

	// Check storage path is set
	if c.StoragePath == "" {
		return fmt.Errorf("storage_path is required")
	}

	return nil
}

// Save writes the current configuration to file
func (c *Config) Save() error {
	if c.ConfigPath == "" {
		return fmt.Errorf("config path not set")
	}

	v := viper.New()
	v.SetConfigFile(c.ConfigPath)

	// Set values
	v.Set("telegram.token", c.Telegram.Token)
	v.Set("telegram.webhook", c.Telegram.Webhook)
	v.Set("ai.provider", c.AI.Provider)
	v.Set("ai.api_key", c.AI.APIKey)
	v.Set("ai.model", c.AI.Model)
	v.Set("ai.base_url", c.AI.BaseURL)
	v.Set("ai.fallback_models", c.AI.FallbackModels)
	v.Set("ai.droid.binary_path", c.AI.Droid.BinaryPath)
	v.Set("ai.droid.auto_level", c.AI.Droid.AutoLevel)
	v.Set("ai.droid.work_dir", c.AI.Droid.WorkDir)
	v.Set("auth.mode", c.Auth.Mode)
	v.Set("auth.allowed_users", c.Auth.AllowedUsers)
	v.Set("auth.admin_id", c.Auth.AdminID)
	v.Set("groups.default_mode", c.Groups.DefaultMode)
	v.Set("api.enabled", c.API.Enabled)
	v.Set("api.port", c.API.Port)
	v.Set("api.bind_addr", c.API.BindAddr)
	v.Set("api.api_key", c.API.APIKey)
	v.Set("api.webhook_chat", c.API.WebhookChat)
	v.Set("tts.provider", c.TTS.Provider)
	v.Set("tts.default_voice", c.TTS.DefaultVoice)
	v.Set("memory.enabled", c.Memory.Enabled)
	v.Set("memory.embeddings_base_url", c.Memory.EmbeddingsBaseURL)
	v.Set("memory.embeddings_api_key", c.Memory.EmbeddingsAPIKey)
	v.Set("memory.embeddings_model", c.Memory.EmbeddingsModel)
	v.Set("memory.metadata_extraction", c.Memory.MetadataExtraction)
	v.Set("memory.metadata_model", c.Memory.MetadataModel)
	v.Set("memory.mcp.enabled", c.Memory.MCP.Enabled)
	v.Set("memory.mcp.host", c.Memory.MCP.Host)
	v.Set("memory.mcp.port", c.Memory.MCP.Port)
	v.Set("memory.mcp.endpoint", c.Memory.MCP.Endpoint)
	v.Set("memory.mcp.allow_writes", c.Memory.MCP.AllowWrites)
	v.Set("storage_path", c.StoragePath)
	v.Set("soul_path", c.SoulPath)
	v.Set("log_level", c.LogLevel)
	v.Set("runtime.session_queue_limit", c.Runtime.SessionQueueLimit)
	if len(c.Runtime.CostTiers) > 0 {
		v.Set("runtime.cost_tiers", c.Runtime.CostTiers)
	}
	if len(c.Runtime.Roles) > 0 {
		v.Set("runtime.roles", c.Runtime.Roles)
	}
	v.Set("session.dm_scope", c.Session.DMScope)

	// Persist fields that were previously omitted causing lossy round-trips.
	if len(c.Models) > 0 {
		v.Set("models", c.Models)
	}
	if len(c.ModelAliases) > 0 {
		v.Set("model_aliases", c.ModelAliases)
	}
	if len(c.Contacts) > 0 {
		v.Set("contacts", c.Contacts)
	}
	v.Set("control.enabled", c.Control.Enabled)
	v.Set("control.port", c.Control.Port)
	v.Set("control.token", c.Control.Token)
	v.Set("control.allow_loopback_without_token", c.Control.AllowLoopbackWithoutToken)
	if len(c.Agents) > 0 {
		v.Set("agents", c.Agents)
	}

	return v.WriteConfig()
}

// GetSoulPath returns the soul path, checking env var first
func (c *Config) GetSoulPath() string {
	// Check environment variable first
	if envPath := os.Getenv("OKGOBOT_SOUL_PATH"); envPath != "" {
		return expandPath(envPath)
	}
	return c.SoulPath
}

// expandPath expands ~ to home directory
func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(homeDir, path[2:])
	}
	return path
}
