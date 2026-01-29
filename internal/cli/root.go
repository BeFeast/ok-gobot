package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"ok-gobot/internal/app"
	"ok-gobot/internal/config"
)

func NewRootCommand(cfg *config.Config, app *app.App) *cobra.Command {
	root := &cobra.Command{
		Use:   "ok-gobot",
		Short: "ok-gobot - Personal AI assistant via Telegram",
		Long: `🦞 ok-gobot - Personal AI assistant via Telegram

A fast Go AI agent bot for Telegram.
Supports Telegram bot integration with personality and memory.`,

		// Update references in other commands
		Version:       "0.1.0",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	// Add commands
	root.AddCommand(newStartCommand(cfg, app))
	root.AddCommand(newConfigCommand(cfg))
	root.AddCommand(newStatusCommand(cfg))
	root.AddCommand(newBrowserCommand())
	root.AddCommand(newVersionCommand())
	root.AddCommand(newDoctorCommand(cfg))
	root.AddCommand(newDaemonCommand(cfg))

	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("ok-gobot v0.1.0 (go)")
		},
	}
}
