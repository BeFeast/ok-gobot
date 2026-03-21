package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"ok-gobot/internal/config"
	"ok-gobot/internal/migrate"
)

func newMigrateCommand(cfg *config.Config) *cobra.Command {
	var (
		sourceDB        string
		targetDB        string
		sourceWorkspace string
		targetWorkspace string
		agentID         string
		dryRun          bool
		backupDir       string
	)

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "One-shot migration from OpenClaw to ok-gobot",
		Long: `Migrate sessions, conversation history, and workspace files from an
OpenClaw bot database to ok-gobot.

What it does:
  1. Reads sessions and message history from the OpenClaw SQLite database.
  2. Creates a timestamped backup of the target (gobot) database.
  3. Inserts sessions and messages into the target database (skips duplicates).
  4. Optionally copies workspace files (soul/personality files) to the
     gobot workspace directory.
  5. Prints a canonical session-key mapping for reference.
  6. Writes a durable markdown report to ~/.ok-gobot/.

Rollback:
  If anything goes wrong, restore the target database from the backup
  printed in the output:
    cp <backup-path> <target-db-path>

Example (dry-run first, then apply):
  ok-gobot migrate --from /path/to/openclaw.db --to /path/to/gobot.db --dry-run
  ok-gobot migrate --from /path/to/openclaw.db --to /path/to/gobot.db`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Default targetDB to configured storage path.
			if targetDB == "" && cfg.StoragePath != "" {
				targetDB = cfg.StoragePath
			}

			opts := migrate.Options{
				SourceDB:        sourceDB,
				TargetDB:        targetDB,
				SourceWorkspace: sourceWorkspace,
				TargetWorkspace: targetWorkspace,
				AgentID:         agentID,
				DryRun:          dryRun,
				BackupDir:       backupDir,
			}

			printMigrateHeader(dryRun)

			report, err := migrate.Run(opts)

			// Write the durable report for both success and failure.
			// On failure the partial report aids debugging.
			if report != nil {
				if reportPath, writeErr := writeReportFile(report, opts); writeErr != nil {
					fmt.Fprintf(os.Stderr, "warning: could not write report file: %v\n", writeErr)
				} else {
					fmt.Printf("Report written to %s\n\n", reportPath)
				}
			}

			if err != nil {
				return err
			}

			printReport(report, opts)
			return nil
		},
	}

	cmd.Flags().StringVar(&sourceDB, "from", "", "Path to the OpenClaw SQLite database (required)")
	cmd.Flags().StringVar(&targetDB, "to", "", "Path to the ok-gobot SQLite database (defaults to storage_path in config)")
	cmd.Flags().StringVar(&sourceWorkspace, "from-workspace", "", "Path to the OpenClaw workspace directory (optional)")
	cmd.Flags().StringVar(&targetWorkspace, "to-workspace", "", "Destination for workspace files (required when --from-workspace is set)")
	cmd.Flags().StringVar(&agentID, "agent", "default", "Agent ID for canonical session key generation")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print planned actions without modifying any files")
	cmd.Flags().StringVar(&backupDir, "backup-dir", "", "Directory for target DB backup (default: <target-dir>/backups)")

	_ = cmd.MarkFlagRequired("from")

	return cmd
}

// writeReportFile writes the markdown report to ~/.ok-gobot/migration-report-YYYY-MM-DD.md
// and returns the path written.
func writeReportFile(r *migrate.Report, opts migrate.Options) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	reportDir := filepath.Join(homeDir, ".ok-gobot")
	if err := os.MkdirAll(reportDir, 0750); err != nil {
		return "", fmt.Errorf("create report directory: %w", err)
	}

	timestamp := time.Now().Format("2006-01-02-150405")
	reportPath := filepath.Join(reportDir, "migration-report-"+timestamp+".md")

	content := r.RenderMarkdown(opts)
	if err := os.WriteFile(reportPath, []byte(content), 0640); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}
	return reportPath, nil
}

func printMigrateHeader(dryRun bool) {
	fmt.Println("🦞 ok-gobot migration — OpenClaw → gobot")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	if dryRun {
		fmt.Printf("%s[DRY-RUN] No files will be written.%s\n", colorYellow, colorReset)
	}
	fmt.Println()
}

func printReport(r *migrate.Report, opts migrate.Options) {
	// Planned actions
	if len(r.Actions) > 0 {
		fmt.Println("Planned actions:")
		for _, a := range r.Actions {
			icon := "  •"
			switch a.Kind {
			case "session":
				icon = colorCyan + "  [session]" + colorReset
			case "message":
				icon = "  [message]"
			case "workspace_file":
				icon = colorGreen + "  [file]   " + colorReset
			}
			fmt.Printf("%s %s\n", icon, a.Summary)
		}
		fmt.Println()
	}

	// Canonical key mapping
	if len(r.KeyMapping) > 0 {
		fmt.Println("Session key mapping (OpenClaw chat_id → gobot canonical key):")
		for _, km := range r.KeyMapping {
			fmt.Printf("  chat_id=%-15d %-10s → %s\n", km.ChatID, "("+km.ChatType+")", km.CanonicalKey)
		}
		fmt.Println()
	}

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	if opts.DryRun {
		fmt.Printf("%s[DRY-RUN COMPLETE] Nothing was written.%s\n", colorYellow, colorReset)
		fmt.Printf("  Sessions planned : %d\n", r.SessionsTotal)
		fmt.Printf("  Messages planned : %d\n", r.MessagesTotal)
		fmt.Printf("  Workspace files  : %d\n", r.WorkspaceFiles)
		fmt.Println()
		fmt.Println("Run without --dry-run to apply.")
		return
	}

	// Backup
	if r.BackupPath != "" {
		fmt.Printf("%s✓ Backup created:%s %s\n", colorGreen, colorReset, r.BackupPath)
		fmt.Println()
		fmt.Println("Rollback instructions:")
		fmt.Printf("  cp %q %q\n", r.BackupPath, opts.TargetDB)
		fmt.Println()
	}

	// Results
	errCount := len(r.Errors)
	color := colorGreen
	if errCount > 0 {
		color = colorYellow
	}

	fmt.Printf("%s✓ Migration complete%s\n", color, colorReset)
	fmt.Printf("  Sessions migrated : %d / %d (skipped: %d)\n", r.SessionsMigrated, r.SessionsTotal, r.SessionsSkipped)
	fmt.Printf("  Messages migrated : %d / %d (skipped: %d)\n", r.MessagesMigrated, r.MessagesTotal, r.MessagesSkipped)
	fmt.Printf("  Workspace files   : %d\n", r.WorkspaceFiles)

	if errCount > 0 {
		fmt.Printf("\n%s%d error(s) occurred:%s\n", colorRed, errCount, colorReset)
		for _, e := range r.Errors {
			fmt.Printf("  • %s\n", strings.TrimSpace(e))
		}
	}
}
