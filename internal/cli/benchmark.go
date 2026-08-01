package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/evidence"
	"ok-gobot/internal/reliability"
	"ok-gobot/internal/storage"
)

const defaultReliabilityManifest = "benchmarks/reliability/fake-scenarios.yaml"
const defaultGitHubReliabilityLookback = 10
const maxGitHubReliabilityLookback = 100
const defaultGitHubReliabilityEvidenceLimit = 200
const defaultGitHubReliabilityPRSearchLimit = 100

type reliabilityBenchmarkDeps struct {
	openStore       func(path string) (reliabilitySessionStore, error)
	newGitHubClient func(dir string) reliability.GitHubClient
}

type reliabilitySessionStore interface {
	Close() error
	ListSessionsV2(limit int) ([]storage.SessionV2, error)
	GetSessionV2(sessionKey string) (*storage.SessionV2, error)
	ListEvidenceEvents(sessionKey string, limit int) ([]evidence.Event, error)
}

type reliabilityBenchmarkOptions struct {
	manifestPath  string
	format        string
	jsonOut       string
	markdownOut   string
	failOnFailure bool
	provider      string
	repo          string
	storagePath   string
	sessionKeys   []string
	lookback      int
	evidenceLimit int
	prSearchLimit int
}

func newBenchmarkCommand(cfg *config.Config) *cobra.Command {
	deps := reliabilityBenchmarkDeps{
		openStore: func(path string) (reliabilitySessionStore, error) {
			return storage.OpenReadOnly(path)
		},
		newGitHubClient: func(dir string) reliability.GitHubClient {
			return reliability.NewGHCLIClient(dir)
		},
	}
	return newBenchmarkCommandWithDeps(cfg, deps)
}

func newBenchmarkCommandWithDeps(cfg *config.Config, deps reliabilityBenchmarkDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Run local benchmark harnesses",
	}
	cmd.AddCommand(newReliabilityBenchmarkCommand(cfg, deps))
	return cmd
}

func newReliabilityBenchmarkCommand(cfg *config.Config, deps reliabilityBenchmarkDeps) *cobra.Command {
	opts := reliabilityBenchmarkOptions{
		manifestPath:  defaultReliabilityManifest,
		format:        "compact",
		provider:      reliability.ProviderFake,
		lookback:      defaultGitHubReliabilityLookback,
		evidenceLimit: defaultGitHubReliabilityEvidenceLimit,
		prSearchLimit: defaultGitHubReliabilityPRSearchLimit,
	}
	if cfg != nil {
		opts.storagePath = cfg.StoragePath
		opts.repo = cfg.Maestro.Repo
	}

	cmd := &cobra.Command{
		Use:   "reliability",
		Short: "Run the autonomous PR lifecycle reliability benchmark",
		Long: `Run a manifest-driven reliability benchmark for the autonomous PR lifecycle.

The default manifest uses deterministic fake scenarios, so it can run locally
without GitHub credentials, Telegram credentials, or live LLM calls.

Use --provider github with --lookback or --session to score recorded Maestro
sessions against read-only GitHub PR lifecycle state.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := runReliabilityBenchmark(cmd, opts, deps)
			if err != nil {
				return err
			}
			if err := renderReliabilityReport(cmd, report, opts); err != nil {
				return err
			}
			if opts.failOnFailure && report.Summary.Failed > 0 {
				return fmt.Errorf("reliability benchmark reported %d failure(s)", report.Summary.Failed)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.manifestPath, "manifest", opts.manifestPath, "path to reliability benchmark manifest for the fake provider")
	cmd.Flags().StringVar(&opts.provider, "provider", opts.provider, "benchmark provider: fake or github")
	cmd.Flags().StringVar(&opts.format, "format", opts.format, "output format: compact, markdown, json")
	cmd.Flags().StringVar(&opts.jsonOut, "json-out", "", "write machine-readable JSON report to this path")
	cmd.Flags().StringVar(&opts.markdownOut, "markdown-out", "", "write human-readable Markdown report to this path")
	cmd.Flags().BoolVar(&opts.failOnFailure, "fail-on-failure", false, "exit non-zero when any scenario is blocked")
	cmd.Flags().StringVar(&opts.repo, "repo", opts.repo, "GitHub repo owner/name for the github provider (default: maestro.repo)")
	cmd.Flags().StringVar(&opts.storagePath, "storage", opts.storagePath, "path to existing ok-gobot SQLite storage for the github provider")
	cmd.Flags().StringSliceVar(&opts.sessionKeys, "session", nil, "explicit session key to score with the github provider; repeat or comma-separate")
	cmd.Flags().IntVar(&opts.lookback, "lookback", opts.lookback, "recent sessions to inspect when --session is not set (github provider)")
	cmd.Flags().IntVar(&opts.evidenceLimit, "evidence-limit", opts.evidenceLimit, "maximum evidence events to read per session (github provider)")
	cmd.Flags().IntVar(&opts.prSearchLimit, "pr-search-limit", opts.prSearchLimit, "maximum recent PRs to scan when matching by linked issue (github provider)")

	return cmd
}

func runReliabilityBenchmark(cmd *cobra.Command, opts reliabilityBenchmarkOptions, deps reliabilityBenchmarkDeps) (reliability.Report, error) {
	switch strings.ToLower(strings.TrimSpace(opts.provider)) {
	case "", reliability.ProviderFake:
		manifest, err := reliability.LoadManifestFile(opts.manifestPath)
		if err != nil {
			return reliability.Report{}, err
		}
		return reliability.NewRunner(nil).Run(cmd.Context(), manifest)
	case reliability.ProviderGitHub:
		return runGitHubReliabilityBenchmark(cmd, opts, deps)
	default:
		return reliability.Report{}, fmt.Errorf("unsupported provider %q (supported: fake, github)", opts.provider)
	}
}

func runGitHubReliabilityBenchmark(cmd *cobra.Command, opts reliabilityBenchmarkOptions, deps reliabilityBenchmarkDeps) (reliability.Report, error) {
	if deps.openStore == nil {
		return reliability.Report{}, fmt.Errorf("github provider requires session storage access")
	}
	if deps.newGitHubClient == nil {
		return reliability.Report{}, fmt.Errorf("github provider requires a GitHub client")
	}
	store, err := deps.openStore(opts.storagePath)
	if err != nil {
		return reliability.Report{}, fmt.Errorf("open read-only session storage: %w", err)
	}
	defer store.Close() //nolint:errcheck

	manifest, err := buildGitHubReliabilityManifest(store, opts)
	if err != nil {
		return reliability.Report{}, err
	}

	client := deps.newGitHubClient("")
	if client == nil {
		return reliability.Report{}, fmt.Errorf("github provider requires a GitHub client")
	}
	if err := client.CheckAuth(cmd.Context()); err != nil {
		return reliability.Report{}, err
	}

	runner := reliability.NewRunner(map[string]reliability.Evaluator{
		reliability.ProviderGitHub: reliability.GitHubEvaluator{
			Client:        client,
			Evidence:      store,
			EvidenceLimit: opts.evidenceLimit,
			PRSearchLimit: opts.prSearchLimit,
		},
	})
	return runner.Run(cmd.Context(), manifest)
}

func buildGitHubReliabilityManifest(store reliabilitySessionStore, opts reliabilityBenchmarkOptions) (reliability.Manifest, error) {
	if store == nil {
		return reliability.Manifest{}, fmt.Errorf("session storage is required")
	}
	if opts.lookback < 0 {
		return reliability.Manifest{}, fmt.Errorf("lookback cannot be negative")
	}
	if opts.lookback > maxGitHubReliabilityLookback {
		return reliability.Manifest{}, fmt.Errorf("lookback %d exceeds maximum %d", opts.lookback, maxGitHubReliabilityLookback)
	}
	if opts.evidenceLimit <= 0 {
		opts.evidenceLimit = defaultGitHubReliabilityEvidenceLimit
	}
	if opts.prSearchLimit <= 0 {
		opts.prSearchLimit = defaultGitHubReliabilityPRSearchLimit
	}

	keys := normalizeSessionKeys(opts.sessionKeys)
	var sessions []storage.SessionV2
	if len(keys) > 0 {
		for _, key := range keys {
			sess, err := store.GetSessionV2(key)
			if err != nil {
				return reliability.Manifest{}, fmt.Errorf("read session %q: %w", key, err)
			}
			if sess == nil {
				return reliability.Manifest{}, fmt.Errorf("session %q not found in storage; pass an existing session key from `ok-gobot sessions list`", key)
			}
			if err := requireSessionEvidence(store, key); err != nil {
				return reliability.Manifest{}, err
			}
			sessions = append(sessions, *sess)
		}
	} else {
		lookback := opts.lookback
		if lookback <= 0 {
			lookback = defaultGitHubReliabilityLookback
		}
		listed, err := store.ListSessionsV2(lookback)
		if err != nil {
			return reliability.Manifest{}, fmt.Errorf("list recent sessions: %w", err)
		}
		if len(listed) == 0 {
			return reliability.Manifest{}, fmt.Errorf("no session state found in storage; pass --session with an existing Maestro session key")
		}
		for _, sess := range listed {
			if hasSessionEvidence(store, sess.SessionKey) {
				sessions = append(sessions, sess)
			}
		}
		if len(sessions) == 0 {
			return reliability.Manifest{}, fmt.Errorf("no sessions with evidence ledger entries found in the last %d sessions; pass --session with a recorded Maestro session", lookback)
		}
	}

	scenarios := make([]reliability.Scenario, 0, len(sessions))
	for i, sess := range sessions {
		scenarios = append(scenarios, reliability.Scenario{
			ID:       githubSessionScenarioID(sess.SessionKey, i),
			Title:    fmt.Sprintf("GitHub evidence for %s", sess.SessionKey),
			Provider: reliability.ProviderGitHub,
			Repo:     strings.TrimSpace(opts.repo),
			Metadata: map[string]string{
				"session_key":     sess.SessionKey,
				"created_at":      sess.CreatedAt,
				"updated_at":      sess.UpdatedAt,
				"evidence_limit":  strconv.Itoa(opts.evidenceLimit),
				"pr_search_limit": strconv.Itoa(opts.prSearchLimit),
			},
		})
	}
	return reliability.Manifest{Name: "github-maestro-sessions", Version: 1, Scenarios: scenarios}, nil
}

func renderReliabilityReport(cmd *cobra.Command, report reliability.Report, opts reliabilityBenchmarkOptions) error {
	jsonBytes, err := report.JSON()
	if err != nil {
		return fmt.Errorf("render JSON report: %w", err)
	}
	if opts.jsonOut != "" {
		if err := os.WriteFile(opts.jsonOut, jsonBytes, 0o644); err != nil {
			return fmt.Errorf("write JSON report: %w", err)
		}
	}
	if opts.markdownOut != "" {
		if err := os.WriteFile(opts.markdownOut, []byte(report.Markdown()), 0o644); err != nil {
			return fmt.Errorf("write Markdown report: %w", err)
		}
	}

	out := cmd.OutOrStdout()
	switch strings.ToLower(strings.TrimSpace(opts.format)) {
	case "", "compact":
		fmt.Fprint(out, report.Compact())
	case "markdown", "md":
		fmt.Fprint(out, report.Markdown())
	case "json":
		fmt.Fprintln(out, string(jsonBytes))
	default:
		return fmt.Errorf("unsupported format %q (supported: compact, markdown, json)", opts.format)
	}
	return nil
}

func requireSessionEvidence(store reliabilitySessionStore, sessionKey string) error {
	if hasSessionEvidence(store, sessionKey) {
		return nil
	}
	return fmt.Errorf("session %q has no evidence ledger entries; run `ok-gobot sessions evidence %s` to inspect available evidence or choose another session", sessionKey, sessionKey)
}

func hasSessionEvidence(store reliabilitySessionStore, sessionKey string) bool {
	events, err := store.ListEvidenceEvents(sessionKey, 1)
	return err == nil && len(events) > 0
}

func normalizeSessionKeys(values []string) []string {
	seen := make(map[string]bool, len(values))
	keys := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			key := strings.TrimSpace(part)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, key)
		}
	}
	return keys
}

func githubSessionScenarioID(sessionKey string, index int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(sessionKey)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
		if b.Len() >= 80 {
			break
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = fmt.Sprintf("session-%d", index+1)
	}
	return "github-" + id
}
