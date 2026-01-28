package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"moltbot/internal/app"
	"moltbot/internal/config"
)

func NewRootCommand(cfg *config.Config, app *app.App) *cobra.Command {
	root := &cobra.Command{
		Use:   "moltbot",
		Short: "Moltbot - Personal AI assistant via Telegram",
		Long: `🦞 Moltbot - Personal AI assistant via Telegram

A lightweight, fast alternative to the TypeScript version.
Supports Telegram bot integration with AI capabilities.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Add commands
	root.AddCommand(newStartCommand(cfg, app))
	root.AddCommand(newConfigCommand(cfg))
	root.AddCommand(newStatusCommand(cfg))
	root.AddCommand(newVersionCommand())

	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Moltbot v0.1.0 (go)")
		},
	}
}
