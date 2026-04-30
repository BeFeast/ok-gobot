package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/memory"
)

type qmdCommandStatus struct {
	State       string
	Reason      string
	Backend     string
	Fallback    string
	Configured  bool
	Diagnostics memory.QMDDiagnostics
}

func newQMDCommand(cfg *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "qmd",
		Short: "Inspect and run explicit QMD sidecar lifecycle checks",
	}
	cmd.AddCommand(newQMDStatusCommand(cfg))
	cmd.AddCommand(newQMDUpdateCommand(cfg))
	cmd.AddCommand(newQMDSmokeCommand(cfg))
	return cmd
}

func newQMDStatusCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show QMD backend availability and fallback state",
		RunE: func(cmd *cobra.Command, args []string) error {
			status := collectQMDCommandStatus(cmd.Context(), cfg)
			printQMDCommandStatus(cmd.OutOrStdout(), status)
			return nil
		},
	}
}

func newQMDUpdateCommand(cfg *config.Config) *cobra.Command {
	return &cobra.Command{
		Use:     "update",
		Aliases: []string{"index"},
		Short:   "Run explicit QMD update/index for configured collections",
		RunE: func(cmd *cobra.Command, args []string) error {
			status := collectQMDCommandStatus(cmd.Context(), cfg)
			out := cmd.OutOrStdout()
			if status.State == "skipped" {
				fmt.Fprintln(out, "QMD update: skipped")
				fmt.Fprintf(out, "Reason: %s\n", status.Reason)
				return nil
			}

			qmd := memory.NewQMDBackend(cliQMDConfig(cfg.Memory.QMD))
			output, err := qmd.Update(cmd.Context())
			if err != nil {
				fmt.Fprintln(out, "QMD update: unavailable")
				fmt.Fprintf(out, "Reason: %v\n", err)
				fmt.Fprintln(out, "Fallback: run `ok-gobot memory index --force` to refresh the built-in backend")
				return err
			}

			fmt.Fprintln(out, "QMD update: used")
			if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
				fmt.Fprintln(out, trimmed)
			}
			return nil
		},
	}
}

func newQMDSmokeCommand(cfg *config.Config) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "smoke [query]",
		Short: "Run a QMD query smoke test and report built-in fallback",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := "ok-gobot memory smoke test"
			if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
				query = strings.TrimSpace(args[0])
			}

			status := collectQMDCommandStatus(cmd.Context(), cfg)
			out := cmd.OutOrStdout()
			if status.State == "skipped" {
				fmt.Fprintln(out, "QMD smoke: skipped")
				fmt.Fprintf(out, "Reason: %s\n", status.Reason)
				return nil
			}

			qmd := memory.NewQMDBackend(cliQMDConfig(cfg.Memory.QMD))
			results, err := qmd.Search(cmd.Context(), query, limit, false)
			if err == nil {
				fmt.Fprintln(out, "QMD: used")
				fmt.Fprintf(out, "Results: %d\n", len(results))
				if len(results) > 0 {
					fmt.Fprintf(out, "First source: %s\n", results[0].Source)
				}
				return nil
			}

			fmt.Fprintln(out, "QMD: unavailable")
			fmt.Fprintf(out, "Reason: %v\n", err)

			store, memStore, openErr := openMemoryStore(cfg)
			if openErr != nil {
				fmt.Fprintf(out, "Fallback: builtin unavailable: %v\n", openErr)
				return openErr
			}
			defer store.Close() //nolint:errcheck
			fallbackResults, fallbackErr := memory.NewBuiltinBackend(nil, memStore).Search(cmd.Context(), query, limit, false)
			if fallbackErr != nil {
				fmt.Fprintf(out, "Fallback: builtin failed: %v\n", fallbackErr)
				return fallbackErr
			}
			fmt.Fprintf(out, "Fallback: builtin used (%d result(s))\n", len(fallbackResults))
			if len(fallbackResults) > 0 {
				fmt.Fprintf(out, "First fallback source: %s\n", fallbackResults[0].Source)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 1, "maximum QMD smoke-test results")
	return cmd
}

func collectQMDCommandStatus(ctx context.Context, cfg *config.Config) qmdCommandStatus {
	status := qmdCommandStatus{State: "skipped", Backend: "builtin", Fallback: "not needed"}
	if cfg == nil {
		status.Reason = "config is nil"
		return status
	}

	status.Backend = memoryBackendName(cfg)
	if !cfg.Memory.Enabled {
		status.Reason = "memory.enabled=false"
		return status
	}
	if status.Backend != "qmd" && status.Backend != "auto" {
		status.Reason = "memory.backend=" + status.Backend
		return status
	}

	status.Configured = true
	status.Fallback = "builtin"
	diagnostics := memory.NewQMDBackend(cliQMDConfig(cfg.Memory.QMD)).Diagnostics(ctx)
	status.Diagnostics = diagnostics
	if !diagnostics.BinaryFound {
		status.State = "unavailable"
		status.Reason = valueOrString(diagnostics.LastError, "qmd binary not found")
		return status
	}
	if !diagnostics.IndexExists {
		status.State = "unavailable"
		status.Reason = valueOrString(diagnostics.LastError, diagnostics.UpdateState)
		if status.Reason == "" {
			status.Reason = "qmd index missing"
		}
		return status
	}

	status.State = "used"
	status.Reason = valueOrString(diagnostics.UpdateState, "primary=qmd")
	return status
}

func printQMDCommandStatus(out qmdStatusWriter, status qmdCommandStatus) {
	fmt.Fprintf(out, "QMD: %s\n", status.State)
	if status.Reason != "" {
		fmt.Fprintf(out, "Reason: %s\n", status.Reason)
	}
	fmt.Fprintf(out, "Memory backend: %s\n", status.Backend)
	fmt.Fprintf(out, "Fallback: %s\n", status.Fallback)

	if !status.Configured {
		return
	}
	d := status.Diagnostics
	fmt.Fprintf(out, "Binary: %s\n", d.BinaryPath)
	fmt.Fprintf(out, "Binary found: %v\n", d.BinaryFound)
	if d.Version != "" {
		fmt.Fprintf(out, "Version: %s\n", d.Version)
	}
	fmt.Fprintf(out, "Search mode: %s\n", d.SearchMode)
	fmt.Fprintf(out, "Index: %s\n", d.IndexName)
	fmt.Fprintf(out, "Index path: %s\n", d.IndexPath)
	fmt.Fprintf(out, "Index exists: %v\n", d.IndexExists)
	if d.UpdateState != "" {
		fmt.Fprintf(out, "Update state: %s\n", d.UpdateState)
	}
	if d.LastError != "" {
		fmt.Fprintf(out, "Last error: %s\n", d.LastError)
	}
}

type qmdStatusWriter interface {
	Write(p []byte) (n int, err error)
}

func valueOrString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
