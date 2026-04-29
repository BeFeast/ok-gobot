package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ok-gobot/internal/config"
)

func newCurateTestConfig(t *testing.T) *config.Config {
	t.Helper()
	soul := t.TempDir()
	if err := os.MkdirAll(filepath.Join(soul, "memory"), 0o755); err != nil {
		t.Fatalf("mkdir memory: %v", err)
	}
	return &config.Config{SoulPath: soul}
}

func writeNote(t *testing.T, soul, date, body string) {
	t.Helper()
	path := filepath.Join(soul, "memory", date+".md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write note %s: %v", date, err)
	}
}

func runCmd(t *testing.T, cfg *config.Config, args ...string) (string, error) {
	t.Helper()
	cmd := newMemoryCommand(cfg)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func extractDraftID(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Draft saved: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Draft saved: "))
		}
	}
	t.Fatalf("could not find draft id in output:\n%s", out)
	return ""
}

func TestMemoryCurateDraft_CreatesDraftWithoutWriting(t *testing.T) {
	cfg := newCurateTestConfig(t)
	writeNote(t, cfg.SoulPath, "2026-04-15", "# Memory: 2026-04-15\n\n- preference: dark mode for everything\n")
	out, err := runCmd(t, cfg, "curate", "--since", "2026-04-15", "--until", "2026-04-15")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Draft saved:") {
		t.Errorf("expected 'Draft saved:' in output: %s", out)
	}
	if _, err := os.Stat(filepath.Join(cfg.SoulPath, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("MEMORY.md must NOT be created by `curate draft`: %v", err)
	}
}

func TestMemoryCurate_NoOpWhenNoNotes(t *testing.T) {
	cfg := newCurateTestConfig(t)
	out, err := runCmd(t, cfg, "curate", "--since", "2026-04-15", "--until", "2026-04-16")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, out)
	}
	if !strings.Contains(out, "No useful candidates") {
		t.Errorf("expected 'No useful candidates' in output: %s", out)
	}
}

func TestMemoryCurate_ApplyRequiresYes(t *testing.T) {
	cfg := newCurateTestConfig(t)
	writeNote(t, cfg.SoulPath, "2026-04-15", "# Memory: 2026-04-15\n\n- decision: ship via Docker\n")
	out, err := runCmd(t, cfg, "curate", "--since", "2026-04-15", "--until", "2026-04-15")
	if err != nil {
		t.Fatalf("draft: %v\n%s", err, out)
	}
	id := extractDraftID(t, out)

	// Apply without --yes must fail and must not write MEMORY.md.
	out2, err2 := runCmd(t, cfg, "curate", "apply", id)
	if err2 == nil {
		t.Fatalf("expected error applying without --yes:\n%s", out2)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.SoulPath, "MEMORY.md")); !os.IsNotExist(statErr) {
		t.Fatal("MEMORY.md should not exist before approval")
	}

	// Apply with --yes succeeds and writes the curated block to MEMORY.md.
	out3, err3 := runCmd(t, cfg, "curate", "apply", id, "--yes")
	if err3 != nil {
		t.Fatalf("apply --yes: %v\n%s", err3, out3)
	}
	if !strings.Contains(out3, "Applied draft") {
		t.Errorf("expected confirmation in output: %s", out3)
	}
	body, err := os.ReadFile(filepath.Join(cfg.SoulPath, "MEMORY.md"))
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	if !strings.Contains(string(body), id) {
		t.Errorf("expected MEMORY.md to contain draft id %s; got:\n%s", id, body)
	}
}

func TestMemoryCurate_RejectKeepsDraftForInspection(t *testing.T) {
	cfg := newCurateTestConfig(t)
	writeNote(t, cfg.SoulPath, "2026-04-15", "# Memory: 2026-04-15\n\n- preference: dark mode\n")
	out, err := runCmd(t, cfg, "curate", "--since", "2026-04-15", "--until", "2026-04-15")
	if err != nil {
		t.Fatalf("draft: %v\n%s", err, out)
	}
	id := extractDraftID(t, out)

	rejectOut, err := runCmd(t, cfg, "curate", "reject", id, "--notes", "noise")
	if err != nil {
		t.Fatalf("reject: %v\n%s", err, rejectOut)
	}
	// Draft files should still exist after rejection.
	if _, err := os.Stat(filepath.Join(cfg.SoulPath, "memory", "drafts", id+".json")); err != nil {
		t.Fatalf("expected rejected draft to be retained on disk: %v", err)
	}
	showOut, err := runCmd(t, cfg, "curate", "show", id)
	if err != nil {
		t.Fatalf("show: %v\n%s", err, showOut)
	}
	if !strings.Contains(showOut, "rejected") {
		t.Errorf("expected status rejected in show output: %s", showOut)
	}

	// Delete must remove both files.
	delOut, err := runCmd(t, cfg, "curate", "delete", id)
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, delOut)
	}
	if _, err := os.Stat(filepath.Join(cfg.SoulPath, "memory", "drafts", id+".json")); !os.IsNotExist(err) {
		t.Fatal("delete should remove the json file")
	}
	if _, err := os.Stat(filepath.Join(cfg.SoulPath, "memory", "drafts", id+".md")); !os.IsNotExist(err) {
		t.Fatal("delete should remove the markdown file")
	}
}

func TestMemoryCurate_AuditBlocksSecretCandidates(t *testing.T) {
	cfg := newCurateTestConfig(t)
	writeNote(t, cfg.SoulPath, "2026-04-15", "# Memory: 2026-04-15\n\n- infra: api_key = sk-leakedvalue\n")
	out, err := runCmd(t, cfg, "curate", "--since", "2026-04-15", "--until", "2026-04-15")
	if err != nil {
		t.Fatalf("draft: %v\n%s", err, out)
	}
	id := extractDraftID(t, out)
	applyOut, err := runCmd(t, cfg, "curate", "apply", id, "--yes")
	if err == nil {
		t.Fatalf("expected audit to block apply, got success:\n%s", applyOut)
	}
	if !strings.Contains(applyOut, "Apply blocked") {
		t.Errorf("expected 'Apply blocked' in output: %s", applyOut)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.SoulPath, "MEMORY.md")); !os.IsNotExist(statErr) {
		t.Fatal("MEMORY.md must not be created when audit blocks apply")
	}
}
