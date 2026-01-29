package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

// Config holds all application configuration
type Config struct {
	ConfigPath  string         `mapstructure:"-"`
	Telegram    TelegramConfig `mapstructure:"telegram"`
	AI          AIConfig       `mapstructure:"ai"`
	Auth        AuthConfig     `mapstructure:"auth"`
	API         APIConfig      `mapstructure:"api"`
	Groups      GroupsConfig   `mapstructure:"groups"`
	TTS         TTSConfig      `mapstructure:"tts"`
	Memory      MemoryConfig   `mapstructure:"memory"`
	Agents      []AgentConfig  `mapstructure:"agents"`
	StoragePath string         `mapstructure:"storage_path"`
	LogLevel    string         `mapstructure:"log_level"`
	SoulPath    string         `mapstructure:"soul_path"` // Path to agent personality files (deprecated, use agents)
}

// TelegramConfig holds Telegram bot configuration
type TelegramConfig struct {
	Token   string `mapstructure:"token"`
	Webhook string `mapstructure:"webhook"`
}

// AIConfig holds AI provider configuration
// Supports: openrouter, openai, or any OpenAI-compatible API
type AIConfig struct {
	Provider        string   `mapstructure:"provider"`         // "openrouter", "openai", "custom"
	APIKey          string   `mapstructure:"api_key"`
	Model           string   `mapstructure:"model"`
	BaseURL         string   `mapstructure:"base_url"`         // For custom providers
	FallbackModels  []string `mapstructure:"fallback_models"`  // Models to try if primary fails
}

// AuthConfig holds authorization configuration
type AuthConfig struct {
	Mode         string  `mapstructure:"mode"`          // "open", "allowlist", "pairing"
	AllowedUsers []int64 `mapstructure:"allowed_users"` // List of allowed Telegram user IDs
	AdminID      int64   `mapstructure:"admin_id"`      // Admin user ID who can manage auth
}

// APIConfig holds HTTP API configuration
type APIConfig struct {
	Enabled    bool   `mapstructure:"enabled"`     // Enable HTTP API server
	Port       int    `mapstructure:"port"`        // API server port
	APIKey     string `mapstructure:"api_key"`     // Required API key for authentication
	WebhookChat int64 `mapstructure:"webhook_chat"` // Default chat ID for webhook messages
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
	Enabled             bool   `mapstructure:"enabled"`               // Enable semantic memory
	EmbeddingsBaseURL   string `mapstructure:"embeddings_base_url"`   // API base URL for embeddings
	EmbeddingsAPIKey    string `mapstructure:"embeddings_api_key"`    // API key for embeddings (can reuse ai.api_key)
	EmbeddingsModel     string `mapstructure:"embeddings_model"`      // Embeddings model to use
}

// AgentConfig holds configuration for a single agent
type AgentConfig struct {
	Name         string   `mapstructure:"name"`
	SoulPath     string   `mapstructure:"soul_path"`
	Model        string   `mapstructure:"model"`          // Empty = use global ai.model
	AllowedTools []string `mapstructure:"allowed_tools"`  // Empty = all tools allowed
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
	v.SetDefault("soul_path", "~/ok-gobot") // Default to visible directory
	v.SetDefault("ai.provider", "openrouter")
	v.SetDefault("ai.model", "moonshotai/kimi-k2.5")
	v.SetDefault("auth.mode", "open")
	v.SetDefault("auth.allowed_users", []int64{})
	v.SetDefault("auth.admin_id", int64(0))
	v.SetDefault("groups.default_mode", "standby")
	v.SetDefault("api.enabled", false)
	v.SetDefault("api.port", 8080)
	v.SetDefault("api.api_key", "")
	v.SetDefault("api.webhook_chat", int64(0))
	v.SetDefault("tts.provider", "openai")
	v.SetDefault("tts.default_voice", "")
	v.SetDefault("memory.enabled", false)
	v.SetDefault("memory.embeddings_base_url", "https://api.openai.com/v1")
	v.SetDefault("memory.embeddings_api_key", "")
	v.SetDefault("memory.embeddings_model", "text-embedding-3-small")

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
	v.SetDefault("soul_path", "~/ok-gobot")
	v.SetDefault("ai.provider", "openrouter")
	v.SetDefault("ai.model", "moonshotai/kimi-k2.5")
	v.SetDefault("auth.mode", "open")
	v.SetDefault("auth.allowed_users", []int64{})
	v.SetDefault("auth.admin_id", int64(0))
	v.SetDefault("groups.default_mode", "standby")
	v.SetDefault("api.enabled", false)
	v.SetDefault("api.port", 8080)
	v.SetDefault("api.api_key", "")
	v.SetDefault("api.webhook_chat", int64(0))
	v.SetDefault("tts.provider", "openai")
	v.SetDefault("tts.default_voice", "")
	v.SetDefault("memory.enabled", false)
	v.SetDefault("memory.embeddings_base_url", "https://api.openai.com/v1")
	v.SetDefault("memory.embeddings_api_key", "")
	v.SetDefault("memory.embeddings_model", "text-embedding-3-small")

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
	if c.AI.APIKey == "" {
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
	v.Set("auth.mode", c.Auth.Mode)
	v.Set("auth.allowed_users", c.Auth.AllowedUsers)
	v.Set("auth.admin_id", c.Auth.AdminID)
	v.Set("groups.default_mode", c.Groups.DefaultMode)
	v.Set("api.enabled", c.API.Enabled)
	v.Set("api.port", c.API.Port)
	v.Set("api.api_key", c.API.APIKey)
	v.Set("api.webhook_chat", c.API.WebhookChat)
	v.Set("tts.provider", c.TTS.Provider)
	v.Set("tts.default_voice", c.TTS.DefaultVoice)
	v.Set("memory.enabled", c.Memory.Enabled)
	v.Set("memory.embeddings_base_url", c.Memory.EmbeddingsBaseURL)
	v.Set("memory.embeddings_api_key", c.Memory.EmbeddingsAPIKey)
	v.Set("memory.embeddings_model", c.Memory.EmbeddingsModel)
	v.Set("storage_path", c.StoragePath)
	v.Set("soul_path", c.SoulPath)
	v.Set("log_level", c.LogLevel)

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
