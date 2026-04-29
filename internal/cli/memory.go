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

			extras, err := extraPathsFromConfig(cfg)
			if err != nil {
				fmt.Fprintf(out, "Extra paths: configuration error: %v\n", err)
				return nil
			}
			if len(extras) == 0 {
				return nil
			}
			fmt.Fprintf(out, "Extra paths (%d):\n", len(extras))
			for _, diag := range memStore.ExtraPathDiagnostics(cmd.Context(), extras) {
				state := "ok"
				if !diag.Available {
					state = "missing"
				}
				fmt.Fprintf(out, "  - %s [%s] path=%s sources=%d chunks=%d read_only=%v\n",
					diag.Name, state, diag.Path, diag.SourceCount, diag.ChunkCount, diag.ReadOnly)
				if diag.Error != "" {
					fmt.Fprintf(out, "    error: %s\n", diag.Error)
				}
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
			var embedder memory.EmbeddingBatchClient
			if memory.EmbeddingProviderConfigured(cfg.Memory.EmbeddingsBaseURL, apiKey) {
				embedder = memory.NewEmbeddingClient(
					cfg.Memory.EmbeddingsBaseURL,
					apiKey,
					cfg.Memory.EmbeddingsModel,
				)
			}
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
		if err := memStore.ClearExtraSources(ctx, ""); err != nil {
			return memory.IndexRunStats{}, err
		}
	}
	indexer := memory.NewIndexer(cfg.GetSoulPath(), memStore, embedder)
	stats, err := memory.IndexManagedSources(ctx, cfg.GetSoulPath(), indexer)
	if err != nil {
		return stats, err
	}

	extras, err := extraPathsFromConfig(cfg)
	if err != nil {
		return stats, err
	}
	if len(extras) == 0 {
		return stats, nil
	}

	extraStats, extraErrs := memory.IndexExtraPaths(ctx, extras, indexer)
	stats.FilesIndexed += extraStats.FilesIndexed
	if len(extraErrs) > 0 {
		// Surface non-fatal extra-path errors; missing/unmounted paths return
		// no error and just contribute zero files.
		return stats, fmt.Errorf("extra path indexing reported %d issue(s); first: %w", len(extraErrs), extraErrs[0])
	}
	return stats, nil
}

// extraPathsFromConfig converts MemoryExtraPathConfig entries into normalized
// ExtraPath values. An empty config returns no entries and no error.
func extraPathsFromConfig(cfg *config.Config) ([]memory.ExtraPath, error) {
	if cfg == nil || len(cfg.Memory.ExtraPaths) == 0 {
		return nil, nil
	}
	raw := make([]memory.RawExtraPath, 0, len(cfg.Memory.ExtraPaths))
	for _, e := range cfg.Memory.ExtraPaths {
		raw = append(raw, memory.RawExtraPath{
			Name:     e.Name,
			Path:     e.Path,
			Patterns: e.Patterns,
			ReadOnly: e.ReadOnly,
			Scope:    e.Scope,
		})
	}
	return memory.NormalizeExtraPaths(raw)
}
