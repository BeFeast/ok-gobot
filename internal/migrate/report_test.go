package migrate_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/migrate"
)

func TestWriteReport_CreatesFile(t *testing.T) {
	dir := t.TempDir()

	report := &migrate.Report{
		BackupPath:       "/tmp/backup/gobot-20260322.db",
		SessionsTotal:    5,
		SessionsMigrated: 3,
		SessionsSkipped:  2,
		MessagesTotal:    20,
		MessagesMigrated: 15,
		MessagesSkipped:  5,
		WorkspaceFiles:   2,
		KeyMapping: []migrate.KeyMapping{
			{ChatID: -100123456, ChatType: "group", CanonicalKey: "agent:default:telegram:group:-100123456"},
			{ChatID: 987654321, ChatType: "private", CanonicalKey: "agent:default:telegram:dm:987654321"},
		},
		Actions: []migrate.Action{
			{Kind: "session", Summary: "import session chat_id=-100123456"},
			{Kind: "message", Summary: "import message chat_id=-100123456 role=user"},
			{Kind: "workspace_file", Summary: "copy workspace file SOUL.md"},
		},
		Errors: []string{"session chat_id=999: some error"},
		DryRun: false,
	}

	opts := migrate.Options{
		SourceDB:        "/path/to/openclaw.db",
		TargetDB:        "/path/to/gobot.db",
		SourceWorkspace: "/path/to/openclaw-ws",
		TargetWorkspace: "/path/to/gobot-ws",
		AgentID:         "default",
	}

	path, err := migrate.WriteReport(report, opts, dir)
	if err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	if !strings.HasPrefix(filepath.Base(path), "migration-report-") {
		t.Errorf("unexpected report filename: %s", filepath.Base(path))
	}
	if !strings.HasSuffix(path, ".md") {
		t.Errorf("report should be markdown, got: %s", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	content := string(data)

	// Verify structured sections exist.
	requiredSections := []string{
		"# Migration Report",
		"## Summary",
		"## Key Mapping",
		"## Sessions",
		"## Workspace Files",
		"## Entries Skipped",
		"## Errors",
		"## Backup",
	}
	for _, s := range requiredSections {
		if !strings.Contains(content, s) {
			t.Errorf("report missing section %q", s)
		}
	}

	// Verify key data is present.
	requiredData := []string{
		"openclaw.db",
		"gobot.db",
		"-100123456",
		"987654321",
		"agent:default:telegram:group:-100123456",
		"agent:default:telegram:dm:987654321",
		"SOUL.md",
		"some error",
	}
	for _, d := range requiredData {
		if !strings.Contains(content, d) {
			t.Errorf("report missing data %q", d)
		}
	}

	// Verify summary table.
	if !strings.Contains(content, "| Sessions |") {
		t.Error("report missing sessions summary row")
	}
	if !strings.Contains(content, "| Messages |") {
		t.Error("report missing messages summary row")
	}
}

func TestWriteReport_DryRunMode(t *testing.T) {
	dir := t.TempDir()

	report := &migrate.Report{
		SessionsTotal:    3,
		SessionsMigrated: 3,
		MessagesTotal:    10,
		MessagesMigrated: 10,
		DryRun:           true,
	}

	opts := migrate.Options{
		SourceDB: "/path/to/openclaw.db",
		TargetDB: "/path/to/gobot.db",
		DryRun:   true,
	}

	path, err := migrate.WriteReport(report, opts, dir)
	if err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "dry-run") {
		t.Error("dry-run report should indicate dry-run mode")
	}
	if !strings.Contains(content, "# Migration Report") {
		t.Error("dry-run report missing header")
	}
}

func TestWriteReport_PartialReport(t *testing.T) {
	dir := t.TempDir()

	// Simulate a partial report from a failed migration.
	report := &migrate.Report{
		SessionsTotal: 5,
		MessagesTotal: 20,
		Errors:        []string{"target DB not accessible"},
	}

	opts := migrate.Options{
		SourceDB: "/path/to/openclaw.db",
		TargetDB: "/path/to/gobot.db",
	}

	path, err := migrate.WriteReport(report, opts, dir)
	if err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "## Errors") {
		t.Error("partial report should contain errors section")
	}
	if !strings.Contains(content, "target DB not accessible") {
		t.Error("partial report should contain error message")
	}
}

func TestWriteReport_MachineParseable(t *testing.T) {
	dir := t.TempDir()

	report := &migrate.Report{
		SessionsTotal:    2,
		SessionsMigrated: 1,
		SessionsSkipped:  1,
		MessagesTotal:    5,
		MessagesMigrated: 3,
		MessagesSkipped:  2,
		KeyMapping: []migrate.KeyMapping{
			{ChatID: -100, ChatType: "group", CanonicalKey: "agent:test:telegram:group:-100"},
		},
	}

	opts := migrate.Options{
		SourceDB: "/path/to/src.db",
		TargetDB: "/path/to/dst.db",
		AgentID:  "test",
	}

	path, err := migrate.WriteReport(report, opts, dir)
	if err != nil {
		t.Fatalf("WriteReport: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	content := string(data)

	// Verify markdown table structure for machine parsing.
	if !strings.Contains(content, "| Metric | Total | Migrated | Skipped |") {
		t.Error("report missing summary table header")
	}
	if !strings.Contains(content, "| chat_id | type | canonical key |") {
		t.Error("report missing key mapping table header")
	}
}
