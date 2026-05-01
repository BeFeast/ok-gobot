package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"ok-gobot/internal/app"
	"ok-gobot/internal/config"
	"ok-gobot/internal/version"
)

func NewRootCommand(cfg *config.Config, app *app.App) *cobra.Command {
	root := &cobra.Command{
		Use:   "ok-gobot",
		Short: "ok-gobot - Personal AI assistant via Telegram",
		Long: `🦞 ok-gobot - Personal AI assistant via Telegram

A fast Go AI agent bot for Telegram.
Supports Telegram bot integration with personality and memory.`,

		// Update references in other commands
		Version:       version.String(),
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
	root.AddCommand(newTUICommand(cfg))
	root.AddCommand(newAuthCommand(cfg))
	root.AddCommand(newEstopCommand(cfg))
	root.AddCommand(newOnboardCommand())
	root.AddCommand(newMigrateCommand(cfg))
	root.AddCommand(newWebCommand(cfg))
	root.AddCommand(newJobsCommand(cfg))
	root.AddCommand(newProvidersCommand(cfg))
	root.AddCommand(newModelsCommand(cfg))
	root.AddCommand(newMemoryCommand(cfg))
	root.AddCommand(newQMDCommand(cfg))
	root.AddCommand(newSkillsCommand(cfg))
	root.AddCommand(newSessionsCommand(cfg))
	root.AddCommand(newWorkCommand(cfg))
	root.AddCommand(newWorktreesCommand(cfg))
	root.AddCommand(newMaestroCommand(cfg))
	root.AddCommand(newBabysitCommand(cfg))
	root.AddCommand(newBatchCommand(cfg))
	root.AddCommand(newVoiceCommand(cfg))
	root.AddCommand(newExportCommand(cfg))
	root.AddCommand(newEvolutionCommand(cfg))
	root.AddCommand(newRolesCommand(cfg))
	root.AddCommand(newBenchmarkCommand())

	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("ok-gobot %s\n", version.String())
		},
	}
}
