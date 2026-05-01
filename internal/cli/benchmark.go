package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/reliability"
)

const defaultReliabilityManifest = "benchmarks/reliability/fake-scenarios.yaml"

func newBenchmarkCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "benchmark",
		Short: "Run local benchmark harnesses",
	}
	cmd.AddCommand(newReliabilityBenchmarkCommand())
	return cmd
}

func newReliabilityBenchmarkCommand() *cobra.Command {
	var (
		manifestPath  string
		format        string
		jsonOut       string
		markdownOut   string
		failOnFailure bool
	)

	cmd := &cobra.Command{
		Use:   "reliability",
		Short: "Run the autonomous PR lifecycle reliability benchmark",
		Long: `Run a manifest-driven reliability benchmark for the autonomous PR lifecycle.

The default manifest uses deterministic fake scenarios, so it can run locally
without GitHub credentials, Telegram credentials, or live LLM calls.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			manifest, err := reliability.LoadManifestFile(manifestPath)
			if err != nil {
				return err
			}

			report, err := reliability.NewRunner(nil).Run(cmd.Context(), manifest)
			if err != nil {
				return err
			}

			jsonBytes, err := report.JSON()
			if err != nil {
				return fmt.Errorf("render JSON report: %w", err)
			}
			if jsonOut != "" {
				if err := os.WriteFile(jsonOut, jsonBytes, 0o644); err != nil {
					return fmt.Errorf("write JSON report: %w", err)
				}
			}
			if markdownOut != "" {
				if err := os.WriteFile(markdownOut, []byte(report.Markdown()), 0o644); err != nil {
					return fmt.Errorf("write Markdown report: %w", err)
				}
			}

			out := cmd.OutOrStdout()
			switch strings.ToLower(strings.TrimSpace(format)) {
			case "", "compact":
				fmt.Fprint(out, report.Compact())
			case "markdown", "md":
				fmt.Fprint(out, report.Markdown())
			case "json":
				fmt.Fprintln(out, string(jsonBytes))
			default:
				return fmt.Errorf("unsupported format %q (supported: compact, markdown, json)", format)
			}

			if failOnFailure && report.Summary.Failed > 0 {
				return fmt.Errorf("reliability benchmark reported %d failure(s)", report.Summary.Failed)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&manifestPath, "manifest", defaultReliabilityManifest, "path to reliability benchmark manifest")
	cmd.Flags().StringVar(&format, "format", "compact", "output format: compact, markdown, json")
	cmd.Flags().StringVar(&jsonOut, "json-out", "", "write machine-readable JSON report to this path")
	cmd.Flags().StringVar(&markdownOut, "markdown-out", "", "write human-readable Markdown report to this path")
	cmd.Flags().BoolVar(&failOnFailure, "fail-on-failure", false, "exit non-zero when any scenario is blocked")

	return cmd
}
