package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/storage"
)

func newMemoryCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Inspect and maintain the markdown memory index",
	}
	cmd.AddCommand(newMemoryStatusCommand(cfg))
	cmd.AddCommand(newMemoryIndexCommand(cfg))
	return cmd
}

func newMemoryStatusCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show memory index status",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, memStore, err := openMemoryStore(cfg)
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck

			status, err := memStore.Status(cmd.Context(), cfg.Memory.Enabled, cfg.GetSoulPath())
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Memory enabled: %v\n", status.Enabled)
			fmt.Fprintf(out, "Root: %s\n", status.RootPath)
			fmt.Fprintf(out, "Sources: %d\n", status.SourceCount)
			fmt.Fprintf(out, "Chunks: %d\n", status.ChunkCount)
			if status.LastIndexedAt != "" {
				fmt.Fprintf(out, "Last indexed: %s\n", status.LastIndexedAt)
			} else {
				fmt.Fprintln(out, "Last indexed: never")
			}
			return nil
		},
	}
}

func newMemoryIndexCommand(cfg *config.Config) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "index",
		Short: "Index managed markdown memory files",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cfg.Memory.Enabled {
				return fmt.Errorf("memory.enabled is false; enable memory before indexing")
			}

			store, memStore, err := openMemoryStore(cfg)
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck

			apiKey := cfg.Memory.EmbeddingsAPIKey
			if apiKey == "" {
				apiKey = cfg.AI.APIKey
			}
			embedder := memory.NewEmbeddingClient(
				cfg.Memory.EmbeddingsBaseURL,
				apiKey,
				cfg.Memory.EmbeddingsModel,
			)
			stats, err := runMemoryIndex(cmd.Context(), cfg, memStore, embedder, force)
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Indexed %d managed memory source file(s)\n", stats.FilesIndexed)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "clear existing managed memory chunks before indexing")
	return cmd
}

func openMemoryStore(cfg *config.Config) (*storage.Store, *memory.MemoryStore, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("config is nil")
	}
	store, err := storage.New(cfg.StoragePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open storage: %w", err)
	}
	memStore, err := memory.NewMemoryStore(store.DB())
	if err != nil {
		store.Close() //nolint:errcheck
		return nil, nil, fmt.Errorf("open memory store: %w", err)
	}
	return store, memStore, nil
}

func runMemoryIndex(ctx context.Context, cfg *config.Config, memStore *memory.MemoryStore, embedder memory.EmbeddingBatchClient, force bool) (memory.IndexRunStats, error) {
	if force {
		if err := memStore.ClearManagedSources(ctx); err != nil {
			return memory.IndexRunStats{}, err
		}
	}
	indexer := memory.NewIndexer(cfg.GetSoulPath(), memStore, embedder)
	return memory.IndexManagedSources(ctx, cfg.GetSoulPath(), indexer)
}
