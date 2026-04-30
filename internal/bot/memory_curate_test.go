package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/config"
	"ok-gobot/internal/memory/curate"
)

func TestParseDaysArg(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"7", 7, false},
		{"7d", 7, false},
		{"  14d ", 14, false},
		{"0", 1, false},    // clamped up to minimum 1
		{"500", 90, false}, // clamped down to max 90
		{"abc", 0, true},
	}
	for _, tc := range cases {
		got, err := parseDaysArg(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseDaysArg(%q): expected error, got %d", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDaysArg(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseDaysArg(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestMemoryCurateUsage_DescribesAllSubcommands(t *testing.T) {
	usage := memoryCurateUsage()
	for _, want := range []string{"draft", "range", "list", "preview", "approve", "reject", "delete"} {
		if !strings.Contains(usage, want) {
			t.Errorf("usage missing subcommand %q:\n%s", want, usage)
		}
	}
	// The literal `yes` confirmation token must be documented so admins know
	// it is required.
	if !strings.Contains(usage, "yes") {
		t.Error("usage should mention the explicit `yes` confirmation token")
	}
}

func TestMemoryCurateCommand_AdminCanListAndPreviewDrafts(t *testing.T) {
	bot, soul := newMemoryCurateCommandTestBot(t, 101)
	draft := saveMemoryCurateTestDraft(t, soul, "20260430-120000-abcdef", []curate.Candidate{
		{
			ID:         "c001",
			Section:    curate.SectionDecision,
			Text:       "decision: use Telegram for memory draft reviews",
			Confidence: 0.91,
			Sources: []curate.Source{{
				Date: "2026-04-29",
				Path: "memory/2026-04-29.md",
				Line: 12,
			}},
		},
		{
			ID:         "c002",
			Section:    curate.SectionStale,
			Text:       "host is old-box",
			Confidence: 0.75,
			Sources: []curate.Source{{
				Date: "2026-04-30",
				Path: "memory/2026-04-30.md",
				Line: 3,
			}},
			Conflicts: []string{"c003"},
		},
	})

	listCtx := newMemoryCurateCommandContext("list", 101, "admin")
	if err := bot.handleMemoryCurateCommand(listCtx); err != nil {
		t.Fatalf("list: %v", err)
	}
	assertSentContains(t, listCtx, draft.ID)
	assertSentContains(t, listCtx, string(curate.StatusPending))

	previewCtx := newMemoryCurateCommandContext("preview "+draft.ID, 101, "admin")
	if err := bot.handleMemoryCurateCommand(previewCtx); err != nil {
		t.Fatalf("preview: %v", err)
	}
	assertSentContains(t, previewCtx, "Source dates: 2026-04-28 -> 2026-04-30")
	assertSentContains(t, previewCtx, "decision: use Telegram for memory draft reviews")
	assertSentContains(t, previewCtx, "source dates: 2026-04-29")
	assertSentContains(t, previewCtx, "risk: conflicts with c003")
	assertSentContains(t, previewCtx, "Audit findings:")
	assertSentContains(t, previewCtx, "conflicting values")
}

func TestMemoryCurateCommand_NonAdminCannotListOrReadDrafts(t *testing.T) {
	bot, soul := newMemoryCurateCommandTestBot(t, 101)
	draft := saveMemoryCurateTestDraft(t, soul, "20260430-120000-abcdef", []curate.Candidate{
		{
			ID:         "c001",
			Section:    curate.SectionPreference,
			Text:       "preference: keep this private",
			Confidence: 0.9,
			Sources:    []curate.Source{{Date: "2026-04-29", Path: "memory/2026-04-29.md"}},
		},
	})

	for _, payload := range []string{"list", "preview " + draft.ID} {
		t.Run(payload, func(t *testing.T) {
			ctx := newMemoryCurateCommandContext(payload, 202, "notadmin")
			if err := bot.handleMemoryCurateCommand(ctx); err != nil {
				t.Fatalf("handleMemoryCurateCommand: %v", err)
			}
			assertSentContains(t, ctx, "admin-only")
			if strings.Contains(strings.Join(ctx.sent, "\n"), "keep this private") {
				t.Fatalf("non-admin response leaked draft content: %#v", ctx.sent)
			}
		})
	}
}

func TestMemoryCurateCommand_InvalidDraftID(t *testing.T) {
	bot, _ := newMemoryCurateCommandTestBot(t, 101)

	ctx := newMemoryCurateCommandContext("preview ../../MEMORY.md", 101, "admin")
	if err := bot.handleMemoryCurateCommand(ctx); err != nil {
		t.Fatalf("handleMemoryCurateCommand: %v", err)
	}
	assertSentContains(t, ctx, "invalid draft id")
}

func TestMemoryCurateCommand_ApproveRequiresConfirmationAndAuditPass(t *testing.T) {
	bot, soul := newMemoryCurateCommandTestBot(t, 101)
	draft := saveMemoryCurateTestDraft(t, soul, "20260430-120000-abcdef", []curate.Candidate{
		{
			ID:         "c001",
			Section:    curate.SectionDecision,
			Text:       "decision: retain the Telegram review flow",
			Confidence: 0.9,
			Sources:    []curate.Source{{Date: "2026-04-29", Path: "memory/2026-04-29.md"}},
		},
	})

	missingYesCtx := newMemoryCurateCommandContext("approve "+draft.ID, 101, "admin")
	if err := bot.handleMemoryCurateCommand(missingYesCtx); err != nil {
		t.Fatalf("approve without yes: %v", err)
	}
	assertSentContains(t, missingYesCtx, "literal `yes`")
	if _, err := os.Stat(filepath.Join(soul, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("MEMORY.md should not exist without confirmation: %v", err)
	}

	blocked := saveMemoryCurateTestDraft(t, soul, "20260430-120100-fedcba", []curate.Candidate{
		{
			ID:         "c001",
			Section:    curate.SectionInfra,
			Text:       "infra: api_key = should-not-be-promoted",
			Confidence: 0.9,
			Sources:    []curate.Source{{Date: "2026-04-30", Path: "memory/2026-04-30.md"}},
		},
	})
	blockedCtx := newMemoryCurateCommandContext("approve "+blocked.ID+" yes", 101, "admin")
	if err := bot.handleMemoryCurateCommand(blockedCtx); err != nil {
		t.Fatalf("approve audit-blocked draft: %v", err)
	}
	assertSentContains(t, blockedCtx, "Apply blocked by audit")
	if _, err := os.Stat(filepath.Join(soul, "MEMORY.md")); !os.IsNotExist(err) {
		t.Fatalf("MEMORY.md should not exist when audit blocks apply: %v", err)
	}

	approvedCtx := newMemoryCurateCommandContext("approve "+draft.ID+" yes", 101, "admin")
	if err := bot.handleMemoryCurateCommand(approvedCtx); err != nil {
		t.Fatalf("approve: %v", err)
	}
	assertSentContains(t, approvedCtx, "Applied draft")
	memoryBytes, err := os.ReadFile(filepath.Join(soul, "MEMORY.md"))
	if err != nil {
		t.Fatalf("read MEMORY.md: %v", err)
	}
	if !strings.Contains(string(memoryBytes), draft.ID) {
		t.Fatalf("MEMORY.md missing applied draft id %s:\n%s", draft.ID, memoryBytes)
	}
}

func TestMemoryCurateCommand_RejectAndDeleteAreAuditableAndDeleteRequiresConfirmation(t *testing.T) {
	bot, soul := newMemoryCurateCommandTestBot(t, 101)
	draft := saveMemoryCurateTestDraft(t, soul, "20260430-120000-abcdef", []curate.Candidate{
		{
			ID:         "c001",
			Section:    curate.SectionMisc,
			Text:       "misc: candidate to reject",
			Confidence: 0.6,
			Sources:    []curate.Source{{Date: "2026-04-29", Path: "memory/2026-04-29.md"}},
		},
	})
	store := curate.NewDraftStore(soul)

	rejectCtx := newMemoryCurateCommandContext("reject "+draft.ID+" not durable", 101, "admin")
	rejectLogs := captureLogs(t, func() {
		if err := bot.handleMemoryCurateCommand(rejectCtx); err != nil {
			t.Fatalf("reject: %v", err)
		}
	})
	assertSentContains(t, rejectCtx, "rejected")
	if !strings.Contains(rejectLogs, "[AUDIT] memory_curate") || !strings.Contains(rejectLogs, `"action":"reject"`) {
		t.Fatalf("reject audit log missing action: %q", rejectLogs)
	}
	loaded, err := store.Load(draft.ID)
	if err != nil {
		t.Fatalf("load rejected draft: %v", err)
	}
	if loaded.Status != curate.StatusRejected {
		t.Fatalf("status = %s, want rejected", loaded.Status)
	}
	if !strings.Contains(loaded.Notes, "rejected by @admin: not durable") {
		t.Fatalf("reject notes missing reviewer label: %q", loaded.Notes)
	}

	missingYesCtx := newMemoryCurateCommandContext("delete "+draft.ID, 101, "admin")
	if err := bot.handleMemoryCurateCommand(missingYesCtx); err != nil {
		t.Fatalf("delete without yes: %v", err)
	}
	assertSentContains(t, missingYesCtx, "literal `yes`")
	if _, err := store.Load(draft.ID); err != nil {
		t.Fatalf("draft should remain without delete confirmation: %v", err)
	}

	deleteCtx := newMemoryCurateCommandContext("delete "+draft.ID+" yes", 101, "admin")
	deleteLogs := captureLogs(t, func() {
		if err := bot.handleMemoryCurateCommand(deleteCtx); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})
	assertSentContains(t, deleteCtx, "deleted")
	if !strings.Contains(deleteLogs, "[AUDIT] memory_curate") || !strings.Contains(deleteLogs, `"action":"delete"`) {
		t.Fatalf("delete audit log missing action: %q", deleteLogs)
	}
	if _, err := store.Load(draft.ID); err == nil {
		t.Fatal("expected deleted draft to be gone")
	}
}

func TestMemoryCurateCommand_DeleteRemovesCorruptDraft(t *testing.T) {
	bot, soul := newMemoryCurateCommandTestBot(t, 101)
	draftID := "20260430-120200-badbad"
	draftsDir := filepath.Join(soul, "memory", "drafts")
	if err := os.MkdirAll(draftsDir, 0o755); err != nil {
		t.Fatalf("create drafts dir: %v", err)
	}
	jsonPath := filepath.Join(draftsDir, draftID+".json")
	markdownPath := filepath.Join(draftsDir, draftID+".md")
	if err := os.WriteFile(jsonPath, []byte("{"), 0o644); err != nil {
		t.Fatalf("write corrupt draft json: %v", err)
	}
	if err := os.WriteFile(markdownPath, []byte("corrupt draft"), 0o644); err != nil {
		t.Fatalf("write draft markdown: %v", err)
	}

	deleteCtx := newMemoryCurateCommandContext("delete "+draftID+" yes", 101, "admin")
	deleteLogs := captureLogs(t, func() {
		if err := bot.handleMemoryCurateCommand(deleteCtx); err != nil {
			t.Fatalf("delete corrupt draft: %v", err)
		}
	})
	assertSentContains(t, deleteCtx, "deleted")
	if !strings.Contains(deleteLogs, "[AUDIT] memory_curate") || !strings.Contains(deleteLogs, `"status":"unknown"`) {
		t.Fatalf("delete audit log missing unknown status: %q", deleteLogs)
	}
	for _, path := range []string{jsonPath, markdownPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("draft artifact should be deleted at %s: %v", path, err)
		}
	}
}

func TestSplitTelegramChunksPreservesUTF8AndLimit(t *testing.T) {
	body := strings.Repeat("a", 3499) + "🙂" + strings.Repeat("b", 50)
	chunks := splitTelegramChunks(body, 3500)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2", len(chunks))
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is not valid UTF-8", i)
		}
		if got := len([]rune(chunk)); got > 3500 {
			t.Fatalf("chunk %d has %d runes, want <= 3500", i, got)
		}
	}
	// This input has no newlines, so boundary newline trimming should not alter it.
	if got := strings.Join(chunks, ""); got != body {
		t.Fatalf("chunks did not preserve newline-free body")
	}
}

func newMemoryCurateCommandTestBot(t *testing.T, adminID int64) (*Bot, string) {
	t.Helper()
	soul := t.TempDir()
	bot := &Bot{
		memory: agent.NewMemory(soul),
		authManager: &AuthManager{
			config: config.AuthConfig{AdminID: adminID},
		},
	}
	return bot, soul
}

func newMemoryCurateCommandContext(payload string, senderID int64, username string) *fakeContext {
	return &fakeContext{
		msg: &telebot.Message{
			Payload: payload,
			Chat:    &telebot.Chat{ID: senderID, Type: telebot.ChatPrivate},
			Sender:  &telebot.User{ID: senderID, Username: username},
		},
	}
}

func saveMemoryCurateTestDraft(t *testing.T, soul, id string, candidates []curate.Candidate) *curate.Draft {
	t.Helper()
	draft := &curate.Draft{
		ID:          id,
		CreatedAt:   time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		Since:       "2026-04-28",
		Until:       "2026-04-30",
		SourceCount: 2,
		Candidates:  candidates,
		Status:      curate.StatusPending,
	}
	if err := curate.NewDraftStore(soul).Save(draft); err != nil {
		t.Fatalf("save draft: %v", err)
	}
	return draft
}

func assertSentContains(t *testing.T, ctx *fakeContext, want string) {
	t.Helper()
	got := strings.Join(ctx.sent, "\n")
	if !strings.Contains(got, want) {
		t.Fatalf("sent response missing %q:\n%s", want, got)
	}
}
