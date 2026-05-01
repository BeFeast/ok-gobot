package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/evidence"
	"ok-gobot/internal/memory"
	"ok-gobot/internal/storage"
)

func newSessionsCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List, fork, search, and inspect conversation sessions",
	}

	cmd.AddCommand(newSessionsListCommand(cfg))
	cmd.AddCommand(newSessionsForkCommand(cfg))
	cmd.AddCommand(newSessionsIndexCommand(cfg))
	cmd.AddCommand(newSessionsSearchCommand(cfg))
	cmd.AddCommand(newSessionsShowCommand(cfg))
	cmd.AddCommand(newSessionsEvidenceCommand(cfg))

	return cmd
}

// --- sessions list ---

func newSessionsListCommand(cfg *config.Config) *cobra.Command {
	var (
		limit     int
		onlyForks bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List conversation sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			sessions, err := store.ListSessionsV2(limit)
			if err != nil {
				return fmt.Errorf("failed to list sessions: %w", err)
			}

			if onlyForks {
				var forks []storage.SessionV2
				for _, s := range sessions {
					if s.ForkedFrom != "" {
						forks = append(forks, s)
					}
				}
				sessions = forks
			}

			if len(sessions) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No sessions found.")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION KEY\tAGENT\tMSGS\tTOKENS\tFORKED FROM\tUPDATED")
			for _, s := range sessions {
				key := truncate(s.SessionKey, 50)
				forkedFrom := ""
				if s.ForkedFrom != "" {
					forkedFrom = truncate(s.ForkedFrom, 40)
				}
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\n",
					key, s.AgentID, s.MessageCount, s.TotalTokens, forkedFrom, formatTime(s.UpdatedAt))
			}
			return w.Flush()
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "maximum number of sessions to show")
	cmd.Flags().BoolVar(&onlyForks, "forks", false, "show only forked sessions")
	return cmd
}

// --- sessions fork ---

func newSessionsForkCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fork <session-key>",
		Short: "Fork a session, creating an independent copy with the full conversation history",
		Long: `Fork creates an independent copy of a session.

The fork starts with the same conversation history as the source session but
diverges independently — changes to either session do not affect the other.

Use 'ok-gobot sessions list' to find available session keys.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sourceKey := strings.TrimSpace(args[0])
			if sourceKey == "" {
				return fmt.Errorf("session key must not be empty")
			}

			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("failed to open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			forkKey, err := store.ForkSessionV2(sourceKey)
			if err != nil {
				return fmt.Errorf("failed to fork session: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Session forked successfully.\n")
			fmt.Fprintf(out, "Source:  %s\n", sourceKey)
			fmt.Fprintf(out, "Fork:    %s\n", forkKey)
			return nil
		},
	}
	return cmd
}

// --- sessions index ---

func newSessionsIndexCommand(cfg *config.Config) *cobra.Command {
	var (
		sessionKey    string
		includeGroups bool
		maxMessages   int
		clear         bool
	)

	cmd := &cobra.Command{
		Use:   "index",
		Short: "Index past session transcripts into the memory search index",
		Long: `Index past session transcripts into the memory search index so they can be
retrieved alongside workspace and daily memory. Indexing is opt-in: it stores
sanitized user/assistant turns and is disabled by default for privacy.

Group sessions are skipped unless --include-groups is given. Use --key to
reindex a specific session, or --clear to drop all indexed transcript
chunks before reindexing.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cfg.Memory.Enabled {
				return fmt.Errorf("memory.enabled is false; enable memory before indexing sessions")
			}

			store, memStore, err := openMemoryStore(cfg)
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck

			ctx := cmd.Context()

			if clear {
				if sessionKey != "" {
					if err := memStore.ClearSessionChunksForKey(ctx, sessionKey); err != nil {
						return fmt.Errorf("clear session chunks: %w", err)
					}
				} else if err := memStore.ClearSessionChunks(ctx); err != nil {
					return fmt.Errorf("clear session chunks: %w", err)
				}
			}

			apiKey := cfg.Memory.EmbeddingsAPIKey
			if apiKey == "" {
				apiKey = cfg.AI.APIKey
			}
			embedder := memory.NewEmbeddingClient(
				cfg.Memory.EmbeddingsBaseURL,
				apiKey,
				cfg.Memory.EmbeddingsModel,
			)

			source := memory.NewSQLiteSessionTranscriptSource(store.DB())
			indexer := memory.NewSessionIndexer(memStore, embedder, source)

			cap := maxMessages
			if cap == 0 {
				cap = cfg.Memory.Sessions.MaxMessagesPerSession
			}
			opts := memory.SessionIndexOptions{
				IncludeGroups:         includeGroups || cfg.Memory.Sessions.IncludeGroups,
				MaxMessagesPerSession: cap,
			}
			if sessionKey != "" {
				opts.OnlyKeys = []string{sessionKey}
			}

			stats, err := indexer.IndexSessions(ctx, opts)
			if err != nil {
				return fmt.Errorf("index sessions: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Sessions considered: %d\n", stats.SessionsConsidered)
			fmt.Fprintf(out, "Sessions indexed:    %d\n", stats.SessionsIndexed)
			fmt.Fprintf(out, "Sessions skipped:    %d\n", stats.SessionsSkipped)
			fmt.Fprintf(out, "Messages indexed:    %d\n", stats.MessagesIndexed)
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionKey, "key", "", "index only the given session key")
	cmd.Flags().BoolVar(&includeGroups, "include-groups", false, "index group-keyed sessions (off by default for privacy)")
	cmd.Flags().IntVar(&maxMessages, "max-messages", 0, "limit messages per session (0 = config default)")
	cmd.Flags().BoolVar(&clear, "clear", false, "remove existing session chunks before indexing")
	return cmd
}

// --- sessions search ---

func newSessionsSearchCommand(cfg *config.Config) *cobra.Command {
	var (
		topK    int
		expand  bool
		sources []string
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search indexed memory across selected sources",
		Long: `Search indexed memory across selected sources (workspace, daily, sessions, extra).

Default sources are session transcripts. Use --source repeatedly to broaden
the search:

  ok-gobot sessions search "kotlin retry" --source session
  ok-gobot sessions search "alice" --source session --source workspace

The output identifies each result by source type, source key (e.g. session
fingerprint), and matched header so callers can expand the original span.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := strings.Join(args, " ")
			if strings.TrimSpace(query) == "" {
				return fmt.Errorf("query must not be empty")
			}
			if !cfg.Memory.Enabled {
				return fmt.Errorf("memory.enabled is false; enable memory before searching")
			}

			store, memStore, err := openMemoryStore(cfg)
			if err != nil {
				return err
			}
			defer store.Close() //nolint:errcheck

			ctx := cmd.Context()
			searcher, err := memory.NewSearcher(ctx, memStore.DB())
			if err != nil {
				return fmt.Errorf("open searcher: %w", err)
			}

			apiKey := cfg.Memory.EmbeddingsAPIKey
			if apiKey == "" {
				apiKey = cfg.AI.APIKey
			}
			embedder := memory.NewEmbeddingClient(
				cfg.Memory.EmbeddingsBaseURL,
				apiKey,
				cfg.Memory.EmbeddingsModel,
			)
			embedding, err := embedder.GetEmbedding(ctx, query)
			if err != nil {
				return fmt.Errorf("embed query: %w", err)
			}

			selected := memory.NormalizeSourceTypes(sources)
			if len(selected) == 0 {
				selected = []memory.SourceType{memory.SourceSession}
			}

			hits := searcher.Search(embedding, memory.SearchOptions{
				TopK:         topK,
				ExpandBranch: expand,
				Sources:      selected,
			})

			out := cmd.OutOrStdout()
			if len(hits) == 0 {
				fmt.Fprintln(out, "No matches.")
				return nil
			}

			for i, snippet := range hits {
				fmt.Fprintf(out, "%d. %s  (score=%.3f)\n", i+1, memory.FormatSnippetCitation(snippet), snippet.Score)
				preview := memory.ClipForOutput(snippet.Text, 240)
				fmt.Fprintf(out, "   %s\n\n", preview)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&topK, "limit", memory.DefaultSearchTopK, "maximum results")
	cmd.Flags().BoolVar(&expand, "expand", false, "expand each match to the full branch (all chunks of the same message/section)")
	cmd.Flags().StringSliceVar(&sources, "source", nil, "limit to source types: workspace, daily, session, extra (repeatable)")
	return cmd
}

// --- sessions show ---

func newSessionsShowCommand(cfg *config.Config) *cobra.Command {
	var (
		around int64
		span   int
		limit  int
	)

	cmd := &cobra.Command{
		Use:   "show <session-key>",
		Short: "Show a span of messages around a search hit",
		Long: `Show a span of messages from a session transcript without loading the full
history. Pair with 'sessions search' to expand around a matched message:

  ok-gobot sessions search "alice" --source session
  ok-gobot sessions show agent:default:telegram:dm:42 --around 102 --span 3

Without --around the most recent (1 + 2*span) messages are shown.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionKey := strings.TrimSpace(args[0])
			if sessionKey == "" {
				return fmt.Errorf("session key must not be empty")
			}

			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			source := memory.NewSQLiteSessionTranscriptSource(store.DB())
			ctx := cmd.Context()
			result, err := memory.LoadSessionSpan(ctx, source, sessionKey, around, span)
			if err != nil {
				return fmt.Errorf("load span: %w", err)
			}
			if len(result.Messages) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No messages.")
				return nil
			}

			messages := result.Messages
			if limit > 0 && limit < len(messages) {
				// Centre the window on the anchor when one is provided.
				if around > 0 {
					anchorIdx := -1
					for i, m := range messages {
						if m.ID == around {
							anchorIdx = i
							break
						}
					}
					if anchorIdx >= 0 {
						start := anchorIdx - limit/2
						if start < 0 {
							start = 0
						}
						end := start + limit
						if end > len(messages) {
							end = len(messages)
							start = end - limit
							if start < 0 {
								start = 0
							}
						}
						messages = messages[start:end]
					} else {
						messages = messages[len(messages)-limit:]
					}
				} else {
					messages = messages[len(messages)-limit:]
				}
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Session: %s (fingerprint=%s)\n", sessionKey, memory.SessionKeyFingerprint(sessionKey))
			fmt.Fprintf(out, "Messages: %d\n\n", len(messages))
			for _, msg := range messages {
				marker := " "
				if msg.ID == around {
					marker = ">"
				}
				clean := memory.SanitizeMessageContent(msg.Content)
				fmt.Fprintf(out, "%s msg %d [%s] @ %s\n", marker, msg.ID, msg.Role, msg.CreatedAt)
				fmt.Fprintf(out, "  %s\n\n", memory.ClipForOutput(clean, 800))
			}
			return nil
		},
	}

	cmd.Flags().Int64Var(&around, "around", 0, "anchor message id to centre the window on")
	cmd.Flags().IntVar(&span, "span", 2, "messages to show before/after the anchor")
	cmd.Flags().IntVar(&limit, "limit", 0, "hard cap on messages to show (0 = no cap)")
	return cmd
}

// --- sessions evidence ---

func newSessionsEvidenceCommand(cfg *config.Config) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "evidence <session-key>",
		Short: "Show the structured evidence timeline for a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionKey := strings.TrimSpace(args[0])
			if sessionKey == "" {
				return fmt.Errorf("session key must not be empty")
			}

			store, err := storage.New(cfg.StoragePath)
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer store.Close() //nolint:errcheck

			events, err := store.ListEvidenceEvents(sessionKey, limit)
			if err != nil {
				return fmt.Errorf("list evidence: %w", err)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Session: %s (fingerprint=%s)\n", sessionKey, memory.SessionKeyFingerprint(sessionKey))
			fmt.Fprintf(out, "Evidence events: %d\n\n", len(events))
			fmt.Fprintln(out, evidence.RenderMarkdown(events, evidence.RenderOptions{Limit: limit}))
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 25, "maximum evidence events to show")
	return cmd
}
