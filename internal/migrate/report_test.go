package migrate_test

import (
	"strings"
	"testing"

	"ok-gobot/internal/migrate"
)

func TestRenderMarkdown_DryRun(t *testing.T) {
	report := &migrate.Report{
		SessionsTotal:    2,
		SessionsMigrated: 2,
		MessagesTotal:    5,
		MessagesMigrated: 5,
		WorkspaceFiles:   3,
		Actions: []migrate.Action{
			{Kind: "session", Summary: "import session chat_id=-100 → agent:default:telegram:group:-100 (messages: 4)"},
			{Kind: "message", Summary: "import message chat_id=-100 role=user (10 chars)"},
			{Kind: "workspace_file", Summary: "copy workspace file SOUL.md"},
		},
		KeyMapping: []migrate.KeyMapping{
			{ChatID: -100, ChatType: "group", CanonicalKey: "agent:default:telegram:group:-100"},
			{ChatID: 42, ChatType: "private", CanonicalKey: "agent:default:telegram:dm:42"},
		},
	}

	opts := migrate.Options{
		SourceDB: "/tmp/openclaw.db",
		TargetDB: "/tmp/gobot.db",
		AgentID:  "default",
		DryRun:   true,
	}

	md := report.RenderMarkdown(opts)

	// Verify structured sections exist.
	for _, want := range []string{
		"# ok-gobot Migration Report",
		"**Mode:** dry-run",
		"**Source DB:** `/tmp/openclaw.db`",
		"**Target DB:** `/tmp/gobot.db`",
		"## Summary",
		"| Sessions | 2 | 2 | 0 |",
		"| Messages | 5 | 5 | 0 |",
		"| Workspace files | 3 | 3 |",
		"## Sessions Imported",
		"## Messages Imported",
		"## Files Copied",
		"## Canonical Key Mapping",
		"| -100 | group |",
		"| 42 | private |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n\nGot:\n%s", want, md)
		}
	}
}

func TestRenderMarkdown_ApplyWithBackup(t *testing.T) {
	report := &migrate.Report{
		BackupPath:       "/backups/gobot-20260321-120000.db",
		SessionsTotal:    3,
		SessionsMigrated: 2,
		SessionsSkipped:  1,
		MessagesTotal:    10,
		MessagesMigrated: 8,
		MessagesSkipped:  2,
		Actions: []migrate.Action{
			{Kind: "session", Summary: "import session chat_id=1"},
		},
		KeyMapping: []migrate.KeyMapping{
			{ChatID: 1, ChatType: "private", CanonicalKey: "agent:bot:telegram:dm:1"},
		},
	}

	opts := migrate.Options{
		SourceDB: "/src.db",
		TargetDB: "/dst.db",
		AgentID:  "bot",
		DryRun:   false,
	}

	md := report.RenderMarkdown(opts)

	for _, want := range []string{
		"**Mode:** apply",
		"## Backup",
		"`/backups/gobot-20260321-120000.db`",
		"**Rollback:**",
		"| Sessions | 3 | 2 | 1 |",
		"| Messages | 10 | 8 | 2 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n\nGot:\n%s", want, md)
		}
	}
}

func TestRenderMarkdown_WithErrors(t *testing.T) {
	report := &migrate.Report{
		SessionsTotal: 1,
		Errors: []string{
			"session chat_id=99: schema mismatch",
			"message chat_id=99 role=user: session not found",
		},
	}

	opts := migrate.Options{
		SourceDB: "/src.db",
		AgentID:  "default",
	}

	md := report.RenderMarkdown(opts)

	if !strings.Contains(md, "## Entries Skipped / Errors") {
		t.Error("missing errors section")
	}
	if !strings.Contains(md, "schema mismatch") {
		t.Error("missing error detail")
	}
}

func TestRenderMarkdown_EmptyReport(t *testing.T) {
	report := &migrate.Report{}
	opts := migrate.Options{SourceDB: "/src.db", AgentID: "default"}

	md := report.RenderMarkdown(opts)

	if !strings.Contains(md, "# ok-gobot Migration Report") {
		t.Error("missing header")
	}
	if !strings.Contains(md, "## Summary") {
		t.Error("missing summary")
	}
	// Should not contain optional sections.
	if strings.Contains(md, "## Backup") {
		t.Error("unexpected backup section for empty report")
	}
	if strings.Contains(md, "## Canonical Key Mapping") {
		t.Error("unexpected key mapping section for empty report")
	}
}

func TestPartialReportOnError(t *testing.T) {
	dir := t.TempDir()
	srcPath := createSourceDB(t, dir)

	// Use a non-existent target without dry-run — will fail at backup/open.
	opts := migrate.Options{
		SourceDB: srcPath,
		TargetDB: "", // empty → triggers error in apply mode
		AgentID:  "default",
		DryRun:   false,
	}

	report, err := migrate.Run(opts)
	if err == nil {
		t.Fatal("expected error for empty target DB path")
	}
	if report == nil {
		t.Fatal("expected partial report on error, got nil")
	}
	// The partial report should have read the sessions before failing.
	if report.SessionsTotal != 2 {
		t.Errorf("partial report: want 2 sessions total, got %d", report.SessionsTotal)
	}
}
