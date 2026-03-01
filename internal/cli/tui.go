package cli

import (
	"github.com/spf13/cobra"

	"ok-gobot/internal/tui"
)

func newTUICommand() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Launch the terminal user interface",
		Long: `Launch the interactive terminal UI.

The session picker lists all active agent sessions.
Press 'n' to open the sub-agent spawn dialog.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return tui.Run(nil)
		},
	}
}
