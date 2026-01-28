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
	StoragePath string         `mapstructure:"storage_path"`
	LogLevel    string         `mapstructure:"log_level"`
}

// TelegramConfig holds Telegram bot configuration
type TelegramConfig struct {
	Token   string `mapstructure:"token"`
	Webhook string `mapstructure:"webhook"`
}

// AIConfig holds AI provider configuration
// Supports: openrouter, openai, or any OpenAI-compatible API
type AIConfig struct {
	Provider string `mapstructure:"provider"` // "openrouter", "openai", "custom"
	APIKey   string `mapstructure:"api_key"`
	Model    string `mapstructure:"model"`
	BaseURL  string `mapstructure:"base_url"` // For custom providers
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
	v.SetDefault("storage_path", "~/.moltbot/moltbot.db")
	v.SetDefault("ai.provider", "openrouter")
	v.SetDefault("ai.model", "moonshotai/kimi-k2.5")

	// Environment variable prefix
	v.SetEnvPrefix("MOLTBOT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Find config directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	configDir := filepath.Join(homeDir, ".moltbot")
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

	// Expand storage path
	cfg.StoragePath = expandPath(cfg.StoragePath)
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
	v.Set("storage_path", c.StoragePath)
	v.Set("log_level", c.LogLevel)

	return v.WriteConfig()
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
