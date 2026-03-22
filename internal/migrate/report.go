package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// WriteReport renders the migration report as markdown and writes it to
// ~/.ok-gobot/migration-report-YYYY-MM-DD.md. It returns the path written.
// When reportDir is non-empty it overrides the default directory (useful for tests).
func WriteReport(r *Report, opts Options, reportDir string) (string, error) {
	if reportDir == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("report: resolve home dir: %w", err)
		}
		reportDir = filepath.Join(homeDir, ".ok-gobot")
	}
	if err := os.MkdirAll(reportDir, 0750); err != nil {
		return "", fmt.Errorf("report: create dir: %w", err)
	}

	datestamp := time.Now().Format("2006-01-02")
	reportPath := filepath.Join(reportDir, "migration-report-"+datestamp+".md")

	content := renderReport(r, opts)

	if err := os.WriteFile(reportPath, []byte(content), 0640); err != nil {
		return "", fmt.Errorf("report: write file: %w", err)
	}
	return reportPath, nil
}

// renderReport builds the markdown string for a migration report.
func renderReport(r *Report, opts Options) string {
	var b strings.Builder

	// Header
	b.WriteString("# Migration Report\n\n")
	if r.DryRun {
		b.WriteString("**Mode:** dry-run (no files were modified)\n\n")
	} else {
		b.WriteString("**Mode:** apply\n\n")
	}
	b.WriteString(fmt.Sprintf("**Date:** %s\n\n", time.Now().Format("2006-01-02 15:04:05 UTC")))
	b.WriteString(fmt.Sprintf("**Source DB:** `%s`\n\n", opts.SourceDB))
	if opts.TargetDB != "" {
		b.WriteString(fmt.Sprintf("**Target DB:** `%s`\n\n", opts.TargetDB))
	}
	if opts.AgentID != "" {
		b.WriteString(fmt.Sprintf("**Agent ID:** `%s`\n\n", opts.AgentID))
	}

	// Backup
	if r.BackupPath != "" {
		b.WriteString("## Backup\n\n")
		b.WriteString(fmt.Sprintf("- Path: `%s`\n", r.BackupPath))
		b.WriteString(fmt.Sprintf("- Rollback: `cp %q %q`\n", r.BackupPath, opts.TargetDB))
		b.WriteString("\n")
	}

	// Summary
	b.WriteString("## Summary\n\n")
	b.WriteString("| Metric | Total | Migrated | Skipped |\n")
	b.WriteString("|--------|------:|---------:|--------:|\n")
	b.WriteString(fmt.Sprintf("| Sessions | %d | %d | %d |\n", r.SessionsTotal, r.SessionsMigrated, r.SessionsSkipped))
	b.WriteString(fmt.Sprintf("| Messages | %d | %d | %d |\n", r.MessagesTotal, r.MessagesMigrated, r.MessagesSkipped))
	b.WriteString(fmt.Sprintf("| Workspace files | %d | — | — |\n", r.WorkspaceFiles))
	b.WriteString("\n")

	// Workspace files (copied)
	if opts.SourceWorkspace != "" {
		b.WriteString("## Workspace Files\n\n")
		b.WriteString(fmt.Sprintf("- Source: `%s`\n", opts.SourceWorkspace))
		if opts.TargetWorkspace != "" {
			b.WriteString(fmt.Sprintf("- Target: `%s`\n", opts.TargetWorkspace))
		}
		fileActions := filterActions(r.Actions, "workspace_file")
		if len(fileActions) > 0 {
			b.WriteString("\n")
			for _, a := range fileActions {
				b.WriteString(fmt.Sprintf("- %s\n", a.Summary))
			}
		}
		b.WriteString("\n")
	}

	// Canonical key mapping
	if len(r.KeyMapping) > 0 {
		b.WriteString("## Key Mapping\n\n")
		b.WriteString("| chat_id | type | canonical key |\n")
		b.WriteString("|--------:|------|---------------|\n")
		for _, km := range r.KeyMapping {
			b.WriteString(fmt.Sprintf("| %d | %s | `%s` |\n", km.ChatID, km.ChatType, km.CanonicalKey))
		}
		b.WriteString("\n")
	}

	// Sessions imported
	sessionActions := filterActions(r.Actions, "session")
	if len(sessionActions) > 0 {
		b.WriteString("## Sessions\n\n")
		for _, a := range sessionActions {
			b.WriteString(fmt.Sprintf("- %s\n", a.Summary))
		}
		b.WriteString("\n")
	}

	// Entries skipped (dedup / errors)
	if r.SessionsSkipped > 0 || r.MessagesSkipped > 0 {
		b.WriteString("## Entries Skipped\n\n")
		if r.SessionsSkipped > 0 {
			b.WriteString(fmt.Sprintf("- **%d session(s)** skipped (already exist in target)\n", r.SessionsSkipped))
		}
		if r.MessagesSkipped > 0 {
			b.WriteString(fmt.Sprintf("- **%d message(s)** skipped (duplicate or missing session)\n", r.MessagesSkipped))
		}
		b.WriteString("\n")
	}

	// Errors
	if len(r.Errors) > 0 {
		b.WriteString("## Errors\n\n")
		for _, e := range r.Errors {
			b.WriteString(fmt.Sprintf("- %s\n", strings.TrimSpace(e)))
		}
		b.WriteString("\n")
	}

	return b.String()
}

// filterActions returns actions matching the given kind.
func filterActions(actions []Action, kind string) []Action {
	var out []Action
	for _, a := range actions {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}
