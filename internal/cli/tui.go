package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/tui"
)

func newTUICommand(cfg *config.Config) *cobra.Command {
	var (
		serverAddr string
		modelList  []string
	)

	defaultControlServerAddr := fmt.Sprintf("127.0.0.1:%d", cfg.Control.Port)
	if cfg.Control.Port == 0 {
		defaultControlServerAddr = "127.0.0.1:8787"
	}

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive terminal UI",
		Long: `Launch the Bubble Tea terminal UI for ok-gobot.

By default this command connects to the running bot control server.
You can point the TUI at a different control server with --server.

Key bindings:
  Enter       Send message
  Alt+Enter   Insert newline
  Ctrl+P      Open session picker
  Ctrl+M      Open model picker
  Ctrl+A      Abort active run
  Ctrl+C      Quit

In-chat commands:
  /abort            Cancel the active AI run
  /new [name]       Open a new session
  /model <name>     Switch to a named model
`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if serverAddr == "" {
				serverAddr = defaultControlServerAddr
			}

			return tui.Run(tui.Options{
				ServerAddr: serverAddr,
				ModelList:  modelList,
			})
		},
	}

	cmd.Flags().StringVar(&serverAddr, "server", "", fmt.Sprintf("control server address (default %s)", defaultControlServerAddr))
	cmd.Flags().StringSliceVar(&modelList, "models", nil, "comma-separated list of models for the model picker")

	return cmd
}
