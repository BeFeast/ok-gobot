package migrate

import (
	"fmt"
	"strings"
	"time"
)

// RenderMarkdown returns the report as a human-readable, machine-parseable
// markdown document. The dryRun flag controls section headers and wording.
func (r *Report) RenderMarkdown(opts Options) string {
	var b strings.Builder
	now := time.Now().UTC().Format("2006-01-02 15:04:05 UTC")

	mode := "apply"
	if opts.DryRun {
		mode = "dry-run"
	}

	fmt.Fprintf(&b, "# ok-gobot Migration Report\n\n")
	fmt.Fprintf(&b, "- **Date:** %s\n", now)
	fmt.Fprintf(&b, "- **Mode:** %s\n", mode)
	fmt.Fprintf(&b, "- **Source DB:** `%s`\n", opts.SourceDB)
	if opts.TargetDB != "" {
		fmt.Fprintf(&b, "- **Target DB:** `%s`\n", opts.TargetDB)
	}
	if opts.AgentID != "" {
		fmt.Fprintf(&b, "- **Agent ID:** %s\n", opts.AgentID)
	}
	b.WriteString("\n")

	// Backup
	if r.BackupPath != "" {
		fmt.Fprintf(&b, "## Backup\n\n")
		fmt.Fprintf(&b, "- **Path:** `%s`\n", r.BackupPath)
		if opts.TargetDB != "" {
			fmt.Fprintf(&b, "- **Rollback:** `cp %q %q`\n", r.BackupPath, opts.TargetDB)
		}
		b.WriteString("\n")
	}

	// Summary
	b.WriteString("## Summary\n\n")
	b.WriteString("| Category | Total | Migrated | Skipped |\n")
	b.WriteString("|----------|------:|---------:|--------:|\n")
	fmt.Fprintf(&b, "| Sessions | %d | %d | %d |\n", r.SessionsTotal, r.SessionsMigrated, r.SessionsSkipped)
	fmt.Fprintf(&b, "| Messages | %d | %d | %d |\n", r.MessagesTotal, r.MessagesMigrated, r.MessagesSkipped)
	fmt.Fprintf(&b, "| Workspace files | %d | %d | — |\n", r.WorkspaceFiles, r.WorkspaceFiles)
	b.WriteString("\n")

	// Sessions imported
	sessionActions := filterActions(r.Actions, "session")
	if len(sessionActions) > 0 {
		b.WriteString("## Sessions Imported\n\n")
		for _, a := range sessionActions {
			fmt.Fprintf(&b, "- %s\n", a.Summary)
		}
		b.WriteString("\n")
	}

	// Messages imported
	messageActions := filterActions(r.Actions, "message")
	if len(messageActions) > 0 {
		b.WriteString("## Messages Imported\n\n")
		for _, a := range messageActions {
			fmt.Fprintf(&b, "- %s\n", a.Summary)
		}
		b.WriteString("\n")
	}

	// Workspace files copied
	fileActions := filterActions(r.Actions, "workspace_file")
	if len(fileActions) > 0 {
		b.WriteString("## Files Copied\n\n")
		for _, a := range fileActions {
			fmt.Fprintf(&b, "- %s\n", a.Summary)
		}
		b.WriteString("\n")
	}

	// Entries skipped (with reason)
	if len(r.Errors) > 0 {
		b.WriteString("## Entries Skipped / Errors\n\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(e))
		}
		b.WriteString("\n")
	}

	// Canonical key mapping
	if len(r.KeyMapping) > 0 {
		b.WriteString("## Canonical Key Mapping\n\n")
		b.WriteString("| Chat ID | Type | Canonical Key |\n")
		b.WriteString("|--------:|------|---------------|\n")
		for _, km := range r.KeyMapping {
			fmt.Fprintf(&b, "| %d | %s | `%s` |\n", km.ChatID, km.ChatType, km.CanonicalKey)
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
