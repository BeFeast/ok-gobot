package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"moltbot/internal/app"
	"moltbot/internal/cli"
	"moltbot/internal/config"
	"moltbot/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize storage
	store, err := storage.New(cfg.StoragePath)
	if err != nil {
		return fmt.Errorf("failed to initialize storage: %w", err)
	}
	defer store.Close()

	// Create context with cancellation for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle interrupt signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		fmt.Println("\nReceived shutdown signal, stopping gracefully...")
		cancel()
	}()

	// Initialize and run the application
	application := app.New(cfg, store)

	// Set up CLI commands
	cmd := cli.NewRootCommand(cfg, application)

	return cmd.ExecuteContext(ctx)
}
