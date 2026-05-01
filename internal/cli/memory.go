package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/memory/curate"
	"ok-gobot/internal/storage"
)

func newMemoryCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "memory",
		Short: "Inspect and maintain the markdown memory index",
	}
	cmd.AddCommand(newMemoryStatusCommand(cfg))
	cmd.AddCommand(newMemoryIndexCommand(cfg))
	cmd.AddCommand(newMemoryCurateCommand(cfg))
	cmd.AddCommand(newMemoryPackCommand(cfg))
	cmd.AddCommand(newMemoryEvalCommand())
	return cmd
}

func newMemoryStatusCommand(cfg *config.Config) *cobra.Command {
	var deep bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show memory index status",
		RunE: func(cmd *cobra.Command, args []string) error {
			var memStore *memory.MemoryStore
			if cfg.Memory.Enabled {
				store, openedMemStore, err := openMemoryStore(cfg)
				if err != nil {
					return err
				}
				memStore = openedMemStore
				defer store.Close() //nolint:errcheck
			}

			watcherState := memory.WatcherStateDisabled
			if cfg.Memory.Enabled {
				watcherState = memory.WatcherStateNotRunning
			}

			status, err := memory.CollectStatus(cmd.Context(), memStore, memory.StatusOptions{
				Enabled:      cfg.Memory.Enabled,
				RootPath:     cfg.GetSoulPath(),
				BackendType:  memoryBackendName(cfg),
				WatcherState: watcherState,
				ExtraPaths:   memoryExtraPathLabels(cfg),
				QMDStatus:    memoryQMDStatus(cfg),
			})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprint(out, memory.FormatStatusCLI(status))

			extras, err := extraPathsFromConfig(cfg)
			if err != nil {
				fmt.Fprintf(out, "Extra paths: configuration error: %v\n", err)
			} else if len(extras) > 0 && memStore != nil {
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
			}
			if deep {
				printDeepMemoryStatus(cmd.Context(), out, cfg)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "include backend diagnostics such as QMD binary, collections, and embedding readiness")
	return cmd
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
			stats, extraErrs, err := runMemoryIndex(cmd.Context(), cfg, memStore, embedder, force)
			if err != nil {
				return err
			}
			for _, e := range extraErrs {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: extra path indexing: %v\n", e)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Indexed %d managed memory source file(s)\n", stats.FilesIndexed)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "clear existing managed memory chunks before indexing")
	return cmd
}

func newMemoryPackCommand(cfg *config.Config) *cobra.Command {
	var budget int
	var limit int
	cmd := &cobra.Command{
		Use:   "pack <query>",
		Short: "Print a cited memory context pack for a query",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cfg.Memory.Enabled {
				return fmt.Errorf("memory.enabled is false; enable memory before building context packs")
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
			var embedder *memory.EmbeddingClient
			if memory.EmbeddingProviderConfigured(cfg.Memory.EmbeddingsBaseURL, apiKey) {
				embedder = memory.NewEmbeddingClient(
					cfg.Memory.EmbeddingsBaseURL,
					apiKey,
					cfg.Memory.EmbeddingsModel,
				)
			}

			var options []memory.MemoryManagerOption
			if backend := memoryBackendName(cfg); backend == "qmd" || backend == "auto" {
				qmdBackend := memory.NewQMDBackend(cliQMDConfig(cfg.Memory.QMD))
				builtin := memory.NewBuiltinBackend(embedder, memStore)
				cooldown := cliDurationOrDefault(cfg.Memory.QMD.FallbackCooldown, time.Minute)
				options = append(options, memory.WithBackend(memory.NewFallbackBackend(qmdBackend, builtin, cooldown)))
			}
			manager := memory.NewMemoryManager(embedder, memStore, options...)

			query := strings.Join(args, " ")
			pack, err := manager.BuildContextPack(cmd.Context(), memory.ContextPackRequest{
				Query: query,
				Scope: memory.ContextPackScope{Surface: "cli"},
				Budget: memory.ContextPackBudget{
					MaxChars:   budget,
					MaxItems:   limit,
					SearchTopK: limit * 2,
				},
			})
			if err != nil {
				return fmt.Errorf("build memory context pack: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprint(out, pack.Text)
			if !strings.HasSuffix(pack.Text, "\n") {
				fmt.Fprintln(out)
			}
			fmt.Fprintf(out, "\nSources used: %s\n", pack.SourceSummary())
			fmt.Fprintf(out, "Results: total=%d included=%d deduped=%d omitted=%d truncated=%v used=%d/%d chars\n",
				pack.TotalResults,
				len(pack.Items),
				pack.Truncation.DedupeDropped,
				pack.Truncation.OmittedResults,
				pack.Truncation.Truncated,
				pack.Truncation.UsedChars,
				pack.Truncation.BudgetChars,
			)
			return nil
		},
	}
	cmd.Flags().IntVar(&budget, "budget", memory.DefaultContextPackMaxChars, "maximum rendered context pack characters")
	cmd.Flags().IntVar(&limit, "limit", memory.DefaultContextPackMaxItems, "maximum cited memory snippets")
	return cmd
}

func newMemoryEvalCommand() *cobra.Command {
	var maxLatency time.Duration
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Run deterministic memory retrieval evaluation fixtures",
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := memory.RunRetrievalEval(cmd.Context(), memory.RetrievalEvalOptions{MaxLatency: maxLatency})
			if err != nil {
				return err
			}

			fmt.Fprint(cmd.OutOrStdout(), report.FormatGateText())
			if !report.Passed() {
				return fmt.Errorf("memory retrieval evaluation failed")
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&maxLatency, "max-latency", memory.DefaultRetrievalEvalMaxLatency, "maximum acceptable latency per golden query")
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

// newMemoryCurateCommand creates the `memory curate` subcommand tree.
//
// `memory curate --since <date> --until <date>` produces a draft promotion
// from daily notes; it never writes to MEMORY.md by itself. The draft is
// stored under <soul>/memory/drafts/ for inspection. Apply requires `--yes`
// for explicit admin approval and a clean audit. Reject keeps the draft for
// inspection; delete removes it.
func newMemoryCurateCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "curate",
		Short: "Curate daily notes into auditable durable-memory promotion drafts",
		Long: "Memory curation reads daily notes from <soul>/memory/*.md and produces " +
			"a draft promotion. Drafts are inspected and audited before any change to " +
			"MEMORY.md. Apply requires explicit admin approval (--yes). Use `list`, " +
			"`show`, `apply`, `reject`, and `delete` subcommands to manage drafts.",
	}
	cmd.AddCommand(newMemoryCurateDraftCommand(cfg))
	cmd.AddCommand(newMemoryCurateListCommand(cfg))
	cmd.AddCommand(newMemoryCurateShowCommand(cfg))
	cmd.AddCommand(newMemoryCurateApplyCommand(cfg))
	cmd.AddCommand(newMemoryCurateRejectCommand(cfg))
	cmd.AddCommand(newMemoryCurateDeleteCommand(cfg))
	// The bare `memory curate --since X --until Y` flow is the most common
	// entry point; mirror it on the parent command so users do not have to
	// type `curate draft`.
	addCurateRangeFlags(cmd, true)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runMemoryCurateDraft(cmd, cfg)
	}
	return cmd
}

func addCurateRangeFlags(cmd *cobra.Command, optional bool) {
	cmd.Flags().String("since", "", "Inclusive start date YYYY-MM-DD")
	cmd.Flags().String("until", "", "Inclusive end date YYYY-MM-DD")
	cmd.Flags().Bool("apply", false, "After producing the draft, apply it (requires --yes)")
	cmd.Flags().Bool("yes", false, "Confirm apply without prompt; required to write MEMORY.md")
	if !optional {
		_ = cmd.MarkFlagRequired("since")
		_ = cmd.MarkFlagRequired("until")
	}
}

func newMemoryCurateDraftCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "draft",
		Short: "Generate a curation draft from the given date range",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMemoryCurateDraft(cmd, cfg)
		},
	}
	addCurateRangeFlags(cmd, false)
	return cmd
}

func runMemoryCurateDraft(cmd *cobra.Command, cfg *config.Config) error {
	soul := cfg.GetSoulPath()
	if soul == "" {
		return fmt.Errorf("soul path is empty; configure soul_path in your config")
	}

	since, _ := cmd.Flags().GetString("since")
	until, _ := cmd.Flags().GetString("until")
	apply, _ := cmd.Flags().GetBool("apply")
	yes, _ := cmd.Flags().GetBool("yes")

	if since == "" || until == "" {
		return fmt.Errorf("--since and --until are required (YYYY-MM-DD)")
	}
	sinceDate, err := curate.ParseDate(since)
	if err != nil {
		return err
	}
	untilDate, err := curate.ParseDate(until)
	if err != nil {
		return err
	}

	curator := curate.NewCurator(soul)
	draft, err := curator.CurateRange(sinceDate, untilDate)
	if err != nil {
		return err
	}

	store := curate.NewDraftStore(soul)
	if err := store.Save(draft); err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	report := curate.AuditDraft(draft)

	fmt.Fprintf(out, "Draft saved: %s\n", draft.ID)
	fmt.Fprintln(out, curate.RenderDraftSummary(draft))
	if draft.IsEmpty() {
		fmt.Fprintln(out, "No useful candidates extracted from the daily notes in this range.")
		return nil
	}
	for _, f := range report.Findings {
		fmt.Fprintf(out, "audit: %s\n", f.String())
	}

	if !apply {
		fmt.Fprintf(out, "\nReview with: ok-gobot memory curate show %s\n", draft.ID)
		fmt.Fprintf(out, "Apply with:  ok-gobot memory curate apply %s --yes\n", draft.ID)
		return nil
	}
	if !yes {
		return fmt.Errorf("--apply requires --yes for explicit admin confirmation; refusing to modify MEMORY.md")
	}
	return runMemoryCurateApply(cmd, cfg, draft.ID, true)
}

func newMemoryCurateListCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List existing curation drafts",
		RunE: func(cmd *cobra.Command, args []string) error {
			soul := cfg.GetSoulPath()
			if soul == "" {
				return fmt.Errorf("soul path is empty")
			}
			store := curate.NewDraftStore(soul)
			summaries, err := store.List()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(summaries) == 0 {
				fmt.Fprintln(out, "No drafts found.")
				return nil
			}
			for _, s := range summaries {
				fmt.Fprintf(out, "%s [%s] %s→%s — %d candidate(s), %d conflict(s) — %s\n",
					s.ID, s.Status, s.Since, s.Until, s.Candidates, s.Conflicts,
					s.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"))
			}
			return nil
		},
	}
}

func newMemoryCurateShowCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "show <draft-id>",
		Short: "Show the rendered draft including audit findings",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			soul := cfg.GetSoulPath()
			store := curate.NewDraftStore(soul)
			d, err := store.Load(strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}
			audit := curate.AuditDraft(d)
			fmt.Fprint(cmd.OutOrStdout(), curate.RenderDraftMarkdown(d, audit))
			return nil
		},
	}
}

func newMemoryCurateApplyCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apply <draft-id>",
		Short: "Apply a draft to MEMORY.md (requires --yes for explicit confirmation)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			yes, _ := cmd.Flags().GetBool("yes")
			return runMemoryCurateApply(cmd, cfg, strings.TrimSpace(args[0]), yes)
		},
	}
	cmd.Flags().Bool("yes", false, "Confirm the apply; required to modify MEMORY.md")
	return cmd
}

func runMemoryCurateApply(cmd *cobra.Command, cfg *config.Config, draftID string, approved bool) error {
	soul := cfg.GetSoulPath()
	if soul == "" {
		return fmt.Errorf("soul path is empty")
	}
	store := curate.NewDraftStore(soul)
	d, audit, err := curate.Apply(soul, store, draftID, curate.ApplyOptions{
		Approved:   approved,
		AdminLabel: "cli",
	})
	out := cmd.OutOrStdout()
	if err != nil {
		switch {
		case errors.Is(err, curate.ErrApprovalRequired):
			fmt.Fprintln(out, "❌ Apply blocked: explicit confirmation required (pass --yes).")
			return err
		case errors.Is(err, curate.ErrAuditBlocked):
			fmt.Fprintln(out, "❌ Apply blocked: audit findings need to be addressed first:")
			for _, f := range audit.Findings {
				if f.Severity == curate.AuditError {
					fmt.Fprintf(out, "  - %s\n", f.String())
				}
			}
			return err
		case errors.Is(err, curate.ErrEmptyDraft):
			fmt.Fprintln(out, "ℹ️ Nothing to promote: draft has no candidates.")
			return err
		case errors.Is(err, curate.ErrAlreadyApplied):
			fmt.Fprintln(out, "ℹ️ Draft was already applied.")
			return err
		}
		return err
	}
	fmt.Fprintf(out, "✅ Applied draft %s to MEMORY.md\n", d.ID)
	for _, f := range audit.Findings {
		if f.Severity == curate.AuditWarning {
			fmt.Fprintf(out, "  warning: %s\n", f.String())
		}
	}
	return nil
}

func newMemoryCurateRejectCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reject <draft-id>",
		Short: "Mark a draft rejected (kept on disk for inspection)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			soul := cfg.GetSoulPath()
			store := curate.NewDraftStore(soul)
			notes, _ := cmd.Flags().GetString("notes")
			d, err := store.SetStatus(strings.TrimSpace(args[0]), curate.StatusRejected, notes)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Draft %s rejected (still on disk; remove with `memory curate delete %s`).\n", d.ID, d.ID)
			return nil
		},
	}
	cmd.Flags().String("notes", "", "Optional reviewer notes recorded with the rejection")
	return cmd
}

func newMemoryCurateDeleteCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "delete <draft-id>",
		Short: "Permanently delete a draft from disk",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			soul := cfg.GetSoulPath()
			store := curate.NewDraftStore(soul)
			id := strings.TrimSpace(args[0])
			if err := store.Delete(id); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Draft %s deleted.\n", id)
			return nil
		},
	}
}

// runMemoryIndex indexes the managed sources and any configured extra paths.
// Errors from individual extra-path entries are returned as a separate slice
// rather than aborting the whole pass — this matches the daemon's behavior
// (see app.startMemoryIndexer) so `ok-gobot memory index` does not fail hard
// while the long-running bot keeps quietly indexing partial results.
func runMemoryIndex(ctx context.Context, cfg *config.Config, memStore *memory.MemoryStore, embedder memory.EmbeddingBatchClient, force bool) (memory.IndexRunStats, []error, error) {
	if force {
		if err := memStore.ClearManagedSources(ctx); err != nil {
			return memory.IndexRunStats{}, nil, err
		}
		if err := memStore.ClearExtraSources(ctx, ""); err != nil {
			return memory.IndexRunStats{}, nil, err
		}
	}
	indexer := memory.NewIndexer(cfg.GetSoulPath(), memStore, embedder)
	stats, err := memory.IndexManagedSources(ctx, cfg.GetSoulPath(), indexer)
	if err != nil {
		return stats, nil, err
	}

	extras, err := extraPathsFromConfig(cfg)
	if err != nil {
		return stats, nil, err
	}
	if len(extras) == 0 {
		return stats, nil, nil
	}

	extraStats, extraErrs := memory.IndexExtraPaths(ctx, extras, indexer)
	stats.FilesIndexed += extraStats.FilesIndexed
	return stats, extraErrs, nil
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

type memoryStatusWriter interface {
	Write(p []byte) (n int, err error)
}

func printDeepMemoryStatus(ctx context.Context, out memoryStatusWriter, cfg *config.Config) {
	qmd := memory.NewQMDBackend(cliQMDConfig(cfg.Memory.QMD))
	d := qmd.Diagnostics(ctx)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "Deep diagnostics:")
	fmt.Fprintln(out, "Built-in backend:")
	fmt.Fprintln(out, "  Type: SQLite + OpenAI-compatible embeddings")
	fmt.Fprintf(out, "  Embeddings model: %s\n", cfg.Memory.EmbeddingsModel)
	fmt.Fprintf(out, "  Embeddings base URL: %s\n", cfg.Memory.EmbeddingsBaseURL)

	fmt.Fprintln(out, "QMD backend:")
	fmt.Fprintf(out, "  Configured: %v\n", memoryBackendName(cfg) == "qmd" || memoryBackendName(cfg) == "auto")
	fmt.Fprintf(out, "  Contract: %s\n", d.StableContract)
	fmt.Fprintf(out, "  Model-safe status: %s\n", d.ModelSafeStatus)
	fmt.Fprintf(out, "  Binary: %s\n", d.BinaryPath)
	fmt.Fprintf(out, "  Binary found: %v\n", d.BinaryFound)
	if d.Version != "" {
		fmt.Fprintf(out, "  Version: %s\n", d.Version)
	}
	fmt.Fprintf(out, "  Search mode: %s\n", d.SearchMode)
	fmt.Fprintf(out, "  Index: %s\n", d.IndexName)
	fmt.Fprintf(out, "  Index path: %s\n", d.IndexPath)
	fmt.Fprintf(out, "  Index exists: %v\n", d.IndexExists)
	fmt.Fprintf(out, "  Update state: %s\n", d.UpdateState)
	fmt.Fprintf(out, "  Embedding ready: %v\n", d.EmbeddingReady)
	fmt.Fprintf(out, "  Vector index: %v\n", d.HasVectorIndex)
	fmt.Fprintf(out, "  Pending embeddings: %d\n", d.NeedsEmbedding)
	fmt.Fprintf(out, "  Total documents: %d\n", d.TotalDocuments)
	if len(d.Collections) == 0 {
		fmt.Fprintln(out, "  Collections: none")
	} else {
		fmt.Fprintln(out, "  Collections:")
		for _, col := range d.Collections {
			state := "missing"
			if col.Present {
				state = "present"
			}
			fmt.Fprintf(out, "    - %s (%s): %s", col.Name, col.Role, state)
			if col.Documents > 0 {
				fmt.Fprintf(out, ", documents=%d", col.Documents)
			}
			if col.Pattern != "" {
				fmt.Fprintf(out, ", pattern=%s", col.Pattern)
			}
			fmt.Fprintln(out)
		}
	}
	if d.LastError != "" {
		fmt.Fprintf(out, "  Last error: %s\n", d.LastError)
	} else {
		fmt.Fprintln(out, "  Last error: none")
	}
}

func memoryBackendName(cfg *config.Config) string {
	if cfg == nil {
		return "builtin"
	}
	name := strings.ToLower(strings.TrimSpace(cfg.Memory.Backend))
	if name == "" {
		return "builtin"
	}
	return name
}

func memoryQMDStatus(cfg *config.Config) string {
	backend := memoryBackendName(cfg)
	if backend == "qmd" || backend == "auto" {
		return "configured"
	}
	return "disabled"
}

func memoryExtraPathLabels(cfg *config.Config) []string {
	if cfg == nil || len(cfg.Memory.ExtraPaths) == 0 {
		return nil
	}
	labels := make([]string, 0, len(cfg.Memory.ExtraPaths))
	for _, extra := range cfg.Memory.ExtraPaths {
		name := strings.TrimSpace(extra.Name)
		path := strings.TrimSpace(extra.Path)
		switch {
		case name != "" && path != "":
			labels = append(labels, fmt.Sprintf("%s=%s", name, path))
		case name != "":
			labels = append(labels, name)
		case path != "":
			labels = append(labels, path)
		}
	}
	return labels
}

func cliQMDConfig(cfg config.MemoryQMDConfig) memory.QMDConfig {
	return memory.QMDConfig{
		BinaryPath: cfg.BinaryPath,
		Index:      cfg.Index,
		IndexPath:  cfg.IndexPath,
		SearchMode: cfg.SearchMode,
		Timeout:    cliDurationOrDefault(cfg.Timeout, memory.DefaultQMDTimeout),
		Collections: memory.QMDCollections{
			Workspace:          cfg.Collections.Workspace,
			DailyNotes:         cfg.Collections.DailyNotes,
			SessionTranscripts: cfg.Collections.SessionTranscripts,
			ExtraPaths:         cfg.Collections.ExtraPaths,
		},
	}
}

func cliDurationOrDefault(value string, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return fallback
	}
	return duration
}
