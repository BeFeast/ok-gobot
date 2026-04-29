package curate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func makeSoulWithNotes(t *testing.T, notes map[string]string) string {
	t.Helper()
	soul := t.TempDir()
	memoryDir := filepath.Join(soul, "memory")
	if err := os.MkdirAll(memoryDir, 0o755); err != nil {
		t.Fatalf("mkdir memory dir: %v", err)
	}
	for date, body := range notes {
		path := filepath.Join(memoryDir, date+".md")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write daily note %s: %v", date, err)
		}
	}
	return soul
}

func mustParseDate(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := ParseDate(s)
	if err != nil {
		t.Fatalf("parse date %s: %v", s, err)
	}
	return d
}

func fixedClock(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

func TestCurateRange_ExtractsCandidatesAndCategorizes(t *testing.T) {
	notes := map[string]string{
		"2026-04-15": "# Memory: 2026-04-15\n\n" +
			"## 09:00\n" +
			"- preference: prefers tea over coffee\n" +
			"- decision: deploy via Docker on Hetzner\n" +
			"- todo: write integration tests for memory curation\n" +
			"- I prefer using Postgres for everything\n",
		"2026-04-16": "# Memory: 2026-04-16\n\n" +
			"## Quick Note (12:34)\n" +
			"infra: Postgres host db.internal:5432\n",
	}
	soul := makeSoulWithNotes(t, notes)

	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC))}
	draft, err := c.CurateRange(mustParseDate(t, "2026-04-15"), mustParseDate(t, "2026-04-16"))
	if err != nil {
		t.Fatalf("CurateRange: %v", err)
	}
	if draft.SourceCount != 2 {
		t.Errorf("SourceCount = %d, want 2", draft.SourceCount)
	}
	if draft.IsEmpty() {
		t.Fatal("draft unexpectedly empty")
	}
	grouped := draft.CandidatesBySection()
	if len(grouped[SectionPreference]) == 0 {
		t.Error("expected at least one preference candidate")
	}
	if len(grouped[SectionDecision]) == 0 {
		t.Error("expected at least one decision candidate")
	}
	if len(grouped[SectionTodo]) == 0 {
		t.Error("expected at least one todo candidate")
	}
	if len(grouped[SectionInfra]) == 0 {
		t.Error("expected at least one infra candidate")
	}

	// Each candidate must include a source reference back to the daily note.
	for _, c := range draft.Candidates {
		if len(c.Sources) == 0 {
			t.Errorf("candidate %s has no sources", c.ID)
			continue
		}
		src := c.Sources[0]
		if !strings.HasPrefix(src.Path, "memory/") {
			t.Errorf("candidate %s source path %q does not start with memory/", c.ID, src.Path)
		}
		if src.Line <= 0 {
			t.Errorf("candidate %s missing line reference", c.ID)
		}
	}
}

func TestCurateRange_EmptyWhenNoNotes(t *testing.T) {
	soul := makeSoulWithNotes(t, nil)
	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC))}
	draft, err := c.CurateRange(mustParseDate(t, "2026-04-15"), mustParseDate(t, "2026-04-16"))
	if err != nil {
		t.Fatalf("CurateRange: %v", err)
	}
	if !draft.IsEmpty() {
		t.Errorf("expected empty draft, got %d candidates", len(draft.Candidates))
	}
	if draft.SourceCount != 0 {
		t.Errorf("SourceCount = %d, want 0", draft.SourceCount)
	}
}

func TestCurateRange_RejectsInvertedRange(t *testing.T) {
	c := &Curator{SoulPath: t.TempDir()}
	_, err := c.CurateRange(mustParseDate(t, "2026-04-16"), mustParseDate(t, "2026-04-15"))
	if err == nil {
		t.Fatal("expected error for inverted range")
	}
}

func TestCurateRange_DetectsConflicts(t *testing.T) {
	notes := map[string]string{
		"2026-04-10": "# Memory: 2026-04-10\n\n- decision: target language is Go\n",
		"2026-04-11": "# Memory: 2026-04-11\n\n- decision: target language is Rust\n",
	}
	soul := makeSoulWithNotes(t, notes)
	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC))}
	draft, err := c.CurateRange(mustParseDate(t, "2026-04-10"), mustParseDate(t, "2026-04-11"))
	if err != nil {
		t.Fatalf("CurateRange: %v", err)
	}
	if !draft.HasConflicts() {
		t.Fatalf("expected draft.HasConflicts(); got candidates: %#v", draft.Candidates)
	}
	// Conflicting candidates should be moved to the stale/conflicting bucket.
	stale := draft.CandidatesBySection()[SectionStale]
	if len(stale) < 2 {
		t.Errorf("expected at least 2 stale candidates from a conflict; got %d", len(stale))
	}
}

func TestAuditDraft_NoOpWhenEmpty(t *testing.T) {
	soul := makeSoulWithNotes(t, nil)
	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC))}
	draft, err := c.CurateRange(mustParseDate(t, "2026-04-15"), mustParseDate(t, "2026-04-16"))
	if err != nil {
		t.Fatalf("CurateRange: %v", err)
	}
	report := AuditDraft(draft)
	if report.HasErrors() {
		t.Errorf("expected no audit errors for empty draft; got %v", report.Findings)
	}
	hasInfo := false
	for _, f := range report.Findings {
		if f.Severity == AuditInfo {
			hasInfo = true
			break
		}
	}
	if !hasInfo {
		t.Error("expected at least one info finding for empty draft")
	}
}

func TestAuditDraft_FlagsSecrets(t *testing.T) {
	notes := map[string]string{
		"2026-04-15": "# Memory: 2026-04-15\n\n- infra: api_key = sk-abcdefghijklmno\n",
	}
	soul := makeSoulWithNotes(t, notes)
	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC))}
	draft, err := c.CurateRange(mustParseDate(t, "2026-04-15"), mustParseDate(t, "2026-04-15"))
	if err != nil {
		t.Fatalf("CurateRange: %v", err)
	}
	report := AuditDraft(draft)
	if !report.HasErrors() {
		t.Errorf("expected audit error for credential pattern; got %v", report.Findings)
	}
}

func TestAuditDraft_FlagsDestructiveSnippets(t *testing.T) {
	notes := map[string]string{
		"2026-04-15": "# Memory: 2026-04-15\n\n- decision: use rm -rf node_modules to reset\n",
	}
	soul := makeSoulWithNotes(t, notes)
	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC))}
	draft, err := c.CurateRange(mustParseDate(t, "2026-04-15"), mustParseDate(t, "2026-04-15"))
	if err != nil {
		t.Fatalf("CurateRange: %v", err)
	}
	report := AuditDraft(draft)
	if !report.HasErrors() {
		t.Errorf("expected audit error for destructive snippet; got %v", report.Findings)
	}
}

func TestApply_RequiresExplicitApproval(t *testing.T) {
	notes := map[string]string{
		"2026-04-15": "# Memory: 2026-04-15\n\n- decision: ship via Docker\n",
	}
	soul := makeSoulWithNotes(t, notes)
	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC))}
	draft, err := c.CurateRange(mustParseDate(t, "2026-04-15"), mustParseDate(t, "2026-04-15"))
	if err != nil {
		t.Fatalf("CurateRange: %v", err)
	}
	store := NewDraftStore(soul)
	if err := store.Save(draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}

	// Approval not granted → must error and must NOT touch MEMORY.md.
	if _, _, err := Apply(soul, store, draft.ID, ApplyOptions{Approved: false}); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Apply(Approved=false) err = %v, want ErrApprovalRequired", err)
	}
	if _, err := os.Stat(filepath.Join(soul, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("MEMORY.md should not exist before approval: %v", err)
	}

	// Approval granted → MEMORY.md is updated, status flips to applied.
	d, _, err := Apply(soul, store, draft.ID, ApplyOptions{Approved: true, AdminLabel: "test-admin"})
	if err != nil {
		t.Fatalf("Apply(Approved=true): %v", err)
	}
	if d.Status != StatusApplied {
		t.Errorf("status after apply = %s, want %s", d.Status, StatusApplied)
	}
	memoryBytes, err := os.ReadFile(filepath.Join(soul, "MEMORY.md"))
	if err != nil {
		t.Fatalf("read MEMORY.md after apply: %v", err)
	}
	if !strings.Contains(string(memoryBytes), draft.ID) {
		t.Errorf("expected MEMORY.md to contain draft id %s; got:\n%s", draft.ID, memoryBytes)
	}

	// A second apply must fail — drafts cannot be applied twice.
	if _, _, err := Apply(soul, store, draft.ID, ApplyOptions{Approved: true}); !errors.Is(err, ErrAlreadyApplied) {
		t.Errorf("second Apply err = %v, want ErrAlreadyApplied", err)
	}
}

func TestApply_BlockedByAuditErrors(t *testing.T) {
	notes := map[string]string{
		"2026-04-15": "# Memory: 2026-04-15\n\n- infra: api_key = sk-deadbeef0000\n",
	}
	soul := makeSoulWithNotes(t, notes)
	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC))}
	draft, err := c.CurateRange(mustParseDate(t, "2026-04-15"), mustParseDate(t, "2026-04-15"))
	if err != nil {
		t.Fatalf("CurateRange: %v", err)
	}
	store := NewDraftStore(soul)
	if err := store.Save(draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}

	// Even with explicit approval, the audit must block apply when secrets
	// are detected — defense in depth.
	if _, _, err := Apply(soul, store, draft.ID, ApplyOptions{Approved: true}); !errors.Is(err, ErrAuditBlocked) {
		t.Fatalf("Apply err = %v, want ErrAuditBlocked", err)
	}
	if _, err := os.Stat(filepath.Join(soul, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatal("MEMORY.md should not be created when audit blocks apply")
	}
}

func TestApply_NoOpWhenEmpty(t *testing.T) {
	soul := makeSoulWithNotes(t, nil)
	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC))}
	draft, err := c.CurateRange(mustParseDate(t, "2026-04-15"), mustParseDate(t, "2026-04-16"))
	if err != nil {
		t.Fatalf("CurateRange: %v", err)
	}
	store := NewDraftStore(soul)
	if err := store.Save(draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	if _, _, err := Apply(soul, store, draft.ID, ApplyOptions{Approved: true}); !errors.Is(err, ErrEmptyDraft) {
		t.Fatalf("Apply(empty draft) err = %v, want ErrEmptyDraft", err)
	}
	if _, err := os.Stat(filepath.Join(soul, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatal("MEMORY.md should not be created for an empty draft")
	}
}

func TestDraftStore_PersistAndList(t *testing.T) {
	notes := map[string]string{
		"2026-04-15": "# Memory: 2026-04-15\n\n- preference: dark mode for everything\n",
	}
	soul := makeSoulWithNotes(t, notes)
	c := &Curator{SoulPath: soul, Now: fixedClock(time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC))}
	d1, err := c.CurateRange(mustParseDate(t, "2026-04-15"), mustParseDate(t, "2026-04-15"))
	if err != nil {
		t.Fatalf("curate: %v", err)
	}
	store := NewDraftStore(soul)
	if err := store.Save(d1); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := store.Load(d1.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != StatusPending {
		t.Errorf("loaded status = %s, want %s", loaded.Status, StatusPending)
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %d, want 1", len(summaries))
	}

	// Reject keeps the draft for inspection.
	if _, err := store.SetStatus(d1.ID, StatusRejected, "noise"); err != nil {
		t.Fatalf("set status: %v", err)
	}
	loaded2, err := store.Load(d1.ID)
	if err != nil {
		t.Fatalf("re-load: %v", err)
	}
	if loaded2.Status != StatusRejected {
		t.Errorf("status = %s, want rejected", loaded2.Status)
	}

	// Explicit Delete removes both files.
	if err := store.Delete(d1.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Load(d1.ID); err == nil {
		t.Fatal("expected error loading deleted draft")
	}
}
