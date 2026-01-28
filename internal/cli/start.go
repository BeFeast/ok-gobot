package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"ok-gobot/internal/app"
	"ok-gobot/internal/config"
)

func newStartCommand(cfg *config.Config, application *app.App) *cobra.Command {
	var daemon bool

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the ok-gobot Telegram bot",
		Long:  `Start the bot and begin processing Telegram messages.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfg.Telegram.Token == "" {
				return fmt.Errorf("telegram token not configured. Run 'ok-gobot config init' first")
			}

			if daemon {
				fmt.Println("Starting ok-gobot in daemon mode...")
				// TODO: Implement daemon mode with proper process management
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			// Setup signal handling
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()

			sigChan := make(chan os.Signal, 1)
			signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigChan
				fmt.Println("\nShutting down...")
				cancel()
			}()

			fmt.Println("🦞 Starting ok-gobot...")
			fmt.Printf("   Config: %s\n", cfg.ConfigPath)

			if err := application.Start(ctx); err != nil {
				return fmt.Errorf("failed to start: %w", err)
			}

			return nil
		},
	}

	cmd.Flags().BoolVarP(&daemon, "daemon", "d", false, "Run as daemon in background")

	return cmd
}
