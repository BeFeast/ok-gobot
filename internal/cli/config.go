package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"moltbot/internal/ai"
	"moltbot/internal/config"
)

func newConfigCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
		Long:  `Initialize and manage Moltbot configuration.`,
	}

	cmd.AddCommand(newConfigInitCommand())
	cmd.AddCommand(newConfigShowCommand(cfg))
	cmd.AddCommand(newConfigSetCommand(cfg))
	cmd.AddCommand(newConfigModelsCommand())

	return cmd
}

func newConfigInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize configuration file",
		RunE: func(cmd *cobra.Command, args []string) error {
			configDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("failed to get home directory: %w", err)
			}

			configPath := filepath.Join(configDir, ".moltbot", "config.yaml")

			if _, err := os.Stat(configPath); err == nil {
				return fmt.Errorf("config already exists at %s", configPath)
			}

			// Create directory
			if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			// Create default config
			defaultConfig := `# Moltbot Configuration
# Get your Telegram bot token from @BotFather

telegram:
  token: ""  # Your Telegram bot token

# AI Provider Configuration
# Supports: openrouter, openai, or any OpenAI-compatible API
ai:
  provider: "openrouter"  # "openrouter", "openai", or "custom"
  api_key: ""            # Your API key (get from openrouter.ai or openai.com)
  model: "moonshotai/kimi-k2.5"  # Model ID
  # base_url: ""         # Optional: for custom providers

# Storage Configuration
storage:
  path: "~/.moltbot/moltbot.db"

# Logging
log:
  level: "info"  # debug, info, warn, error
`
			if err := os.WriteFile(configPath, []byte(defaultConfig), 0600); err != nil {
				return fmt.Errorf("failed to write config: %w", err)
			}

			fmt.Printf("✅ Created config at %s\n", configPath)
			fmt.Println("\nNext steps:")
			fmt.Println("1. Get a bot token from @BotFather on Telegram")
			fmt.Println("2. Get an API key from openrouter.ai (free credits available)")
			fmt.Println("3. Edit the config: moltbot config set telegram.token <token>")
			fmt.Println("4. Set AI key: moltbot config set ai.api_key <key>")
			fmt.Println("5. Start the bot: moltbot start")

			return nil
		},
	}
}

func newConfigShowCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("Config file: %s\n", cfg.ConfigPath)
			fmt.Printf("\nTelegram:\n")
			fmt.Printf("  Token: %s\n", maskToken(cfg.Telegram.Token))
			fmt.Printf("\nAI Provider:\n")
			fmt.Printf("  Provider: %s\n", cfg.AI.Provider)
			fmt.Printf("  Model: %s\n", cfg.AI.Model)
			fmt.Printf("  API Key: %s\n", maskToken(cfg.AI.APIKey))
			if cfg.AI.BaseURL != "" {
				fmt.Printf("  Base URL: %s\n", cfg.AI.BaseURL)
			}
			fmt.Printf("\nStorage:\n")
			fmt.Printf("  Path: %s\n", cfg.StoragePath)
		},
	}
}

func newConfigSetCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key := args[0]
			value := args[1]

			// Simple key-value setting
			switch key {
			case "telegram.token":
				cfg.Telegram.Token = value
			case "ai.provider":
				cfg.AI.Provider = value
			case "ai.api_key":
				cfg.AI.APIKey = value
			case "ai.model":
				cfg.AI.Model = value
			case "ai.base_url":
				cfg.AI.BaseURL = value
			// Legacy support
			case "openai.api_key":
				cfg.AI.APIKey = value
				cfg.AI.Provider = "openai"
			case "openai.model":
				cfg.AI.Model = value
			default:
				return fmt.Errorf("unknown config key: %s", key)
			}

			if err := cfg.Save(); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Printf("✅ Set %s\n", key)
			return nil
		},
	}
}

func newConfigModelsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "Show available AI models",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("🤖 Available AI Models:\n")

			models := ai.AvailableModels()

			fmt.Println("OpenRouter (Recommended):")
			fmt.Println("  Get API key: https://openrouter.ai/keys")
			for _, model := range models["openrouter"] {
				fmt.Printf("    - %s\n", model)
			}

			fmt.Println("\nOpenAI:")
			fmt.Println("  Get API key: https://platform.openai.com/api-keys")
			for _, model := range models["openai"] {
				fmt.Printf("    - %s\n", model)
			}

			fmt.Println("\nUsage:")
			fmt.Println("  moltbot config set ai.provider openrouter")
			fmt.Println("  moltbot config set ai.model moonshotai/kimi-k2.5")
			fmt.Println("  moltbot config set ai.api_key <your-key>")
		},
	}
}

func maskToken(token string) string {
	if token == "" {
		return "(not set)"
	}
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "..." + token[len(token)-4:]
}
