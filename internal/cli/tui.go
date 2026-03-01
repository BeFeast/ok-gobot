package cli

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
	"ok-gobot/internal/controlserver"
	"ok-gobot/internal/tui"
)

const defaultControlServerAddr = "127.0.0.1:9099"

func newTUICommand(cfg *config.Config) *cobra.Command {
	var (
		serverAddr string
		noServer   bool
		modelList  []string
	)

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Launch the interactive terminal UI",
		Long: `Launch the Bubble Tea terminal UI for ok-gobot.

By default this command starts a local control server and connects
the TUI to it. You can point the TUI at an existing control server
with --server.

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
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			if serverAddr == "" {
				serverAddr = defaultControlServerAddr
			}

			if !noServer {
				// Start an embedded control server
				aiCfg := ai.ProviderConfig{
					Name:    cfg.AI.Provider,
					APIKey:  cfg.AI.APIKey,
					Model:   cfg.AI.Model,
					BaseURL: cfg.AI.BaseURL,
				}

				srv := controlserver.New(controlserver.Config{
					Addr:  serverAddr,
					AICfg: aiCfg,
				})

				srvCtx, srvCancel := context.WithCancel(ctx)
				defer srvCancel()

				go func() {
					if err := srv.Start(srvCtx); err != nil && srvCtx.Err() == nil {
						log.Printf("[tui] control server error: %v", err)
					}
				}()

				// Wait for the server to be ready (up to 3 s)
				if err := controlserver.WaitReady(serverAddr, 3*time.Second); err != nil {
					return fmt.Errorf("control server did not start: %w", err)
				}
			}

			return tui.Run(tui.Options{
				ServerAddr: serverAddr,
				ModelList:  modelList,
			})
		},
	}

	cmd.Flags().StringVar(&serverAddr, "server", "", fmt.Sprintf("control server address (default %s)", defaultControlServerAddr))
	cmd.Flags().BoolVar(&noServer, "no-server", false, "do not start an embedded control server (use --server to point at one)")
	cmd.Flags().StringSliceVar(&modelList, "models", nil, "comma-separated list of models for the model picker")

	return cmd
}
