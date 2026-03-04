package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"ok-gobot/internal/bootstrap"
)

func newOnboardCommand() *cobra.Command {
	var soulPath string

	cmd := &cobra.Command{
		Use:   "onboard",
		Short: "First-time setup wizard",
		Long: `Interactive setup for ok-gobot.

This wizard will:
1. Configure the agent's personality files location
2. Help you set up Telegram bot token
3. Configure AI provider (OpenRouter/OpenAI/Anthropic)
4. Set up Chrome browser for automation`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("🦞 Welcome to ok-gobot Setup!")
			fmt.Println("================================")

			if soulPath == "" {
				soulPath = bootstrap.DefaultPath
			}
			soulPath = bootstrap.ExpandPath(soulPath)

			fmt.Printf("📁 Agent personality files will be stored in: %s\n", soulPath)
			fmt.Println("\nThis directory will contain:")
			fmt.Println("  - SOUL.md        (who you are)")
			fmt.Println("  - IDENTITY.md    (your name, vibe)")
			fmt.Println("  - USER.md        (Oleg's profile)")
			fmt.Println("  - AGENTS.md      (operating rules)")
			fmt.Println("  - TOOLS.md       (SSH hosts, local notes)")
			fmt.Println("  - MEMORY.md      (long-term memory)")
			fmt.Println("  - HEARTBEAT.md   (periodic checks)")
			fmt.Println("  - memory/        (daily notes)")
			fmt.Println("  - chrome-profile/ (browser data)")

			report, err := bootstrap.Scaffold(soulPath)
			if err != nil {
				return fmt.Errorf("failed to create bootstrap files: %w", err)
			}

			if len(report.CreatedFiles) > 0 || len(report.CreatedDirs) > 0 {
				fmt.Printf("\n✅ Created %d bootstrap file(s)\n", len(report.CreatedFiles))
			} else {
				fmt.Printf("\n✅ Directory already exists at %s\n", soulPath)
			}

			configPath, err := defaultConfigPath()
			if err != nil {
				return fmt.Errorf("failed to resolve config path: %w", err)
			}

			createdConfig, err := ensureDefaultConfig(configPath, soulPath)
			if err != nil {
				return fmt.Errorf("failed to create default config: %w", err)
			}

			if createdConfig {
				fmt.Printf("✅ Created config at %s\n", configPath)
			} else {
				fmt.Printf("ℹ️ Existing config preserved at %s\n", configPath)
			}

			fmt.Printf("\n🎯 Bootstrap ready!\n")
			fmt.Printf("Agent files location: %s\n", soulPath)
			fmt.Println("\nNext steps:")
			fmt.Printf("  1. Edit %s/IDENTITY.md to set your name\n", soulPath)
			fmt.Printf("  2. Edit %s/SOUL.md to define your personality\n", soulPath)
			fmt.Println("  3. Run: ok-gobot config set telegram.token <token>")
			fmt.Println("  4. Run: ok-gobot config set ai.api_key <key>")
			fmt.Println("     Or:  ok-gobot auth anthropic login")
			fmt.Println("  5. Run: ok-gobot doctor")
			fmt.Println("  6. Run: ok-gobot start")

			return nil
		},
	}

	cmd.Flags().StringVarP(&soulPath, "path", "p", bootstrap.DefaultPath, "Path to agent personality files")

	return cmd
}
