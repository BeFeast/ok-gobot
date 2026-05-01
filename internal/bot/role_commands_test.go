package bot

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v4"

	artifactview "ok-gobot/internal/artifacts"
	"ok-gobot/internal/storage"
)

func TestJobStatusIcon(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"pending", "⏳"},
		{"running", "🏃"},
		{"succeeded", "✅"},
		{"failed", "❌"},
		{"cancelled", "🛑"},
		{"timed_out", "⏰"},
		{"unknown", "🧾"},
	}
	for _, tc := range tests {
		if got := jobStatusIcon(tc.status); got != tc.want {
			t.Errorf("jobStatusIcon(%q) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestParseJobTime(t *testing.T) {
	tests := []struct {
		input string
		valid bool
	}{
		{"2024-01-15 09:30:00", true},
		{"2024-01-15T09:30:00Z", true},
		{"2024-01-15T09:30:00+00:00", true},
		{"not-a-time", false},
		{"", false},
	}
	for _, tc := range tests {
		got, err := parseJobTime(tc.input)
		if tc.valid && err != nil {
			t.Errorf("parseJobTime(%q) unexpected error: %v", tc.input, err)
		}
		if !tc.valid && err == nil {
			t.Errorf("parseJobTime(%q) expected error, got %v", tc.input, got)
		}
		if tc.valid && got.IsZero() {
			t.Errorf("parseJobTime(%q) returned zero time", tc.input)
		}
	}
}

func TestParseJobTime_CorrectValue(t *testing.T) {
	got, err := parseJobTime("2024-03-15 14:30:00")
	if err != nil {
		t.Fatalf("parseJobTime error: %v", err)
	}
	want := time.Date(2024, 3, 15, 14, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("parseJobTime = %v, want %v", got, want)
	}
}

func TestLoadBotRoles_NilPath(t *testing.T) {
	b := &Bot{}
	roles, err := b.loadBotRoles()
	if err != nil {
		t.Fatalf("loadBotRoles error: %v", err)
	}
	// Should still return bundled roles.
	if len(roles) == 0 {
		t.Error("expected at least bundled roles, got 0")
	}

	// Verify we can find a known bundled role.
	found := false
	for _, m := range roles {
		if m.Name == "researcher" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected bundled 'researcher' role")
	}
}

func TestFindBotRole_Found(t *testing.T) {
	b := &Bot{}
	m, err := b.findBotRole("researcher")
	if err != nil {
		t.Fatalf("findBotRole error: %v", err)
	}
	if m.Name != "researcher" {
		t.Errorf("expected name 'researcher', got %q", m.Name)
	}
}

func TestFindBotRole_NotFound(t *testing.T) {
	b := &Bot{}
	_, err := b.findBotRole("nonexistent-role-xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent role")
	}
}

func TestFormatRoleJobAckIncludesWorkerAndJobHint(t *testing.T) {
	out := formatRoleJobAck("job-123", "researcher", "premium")
	for _, want := range []string{"Role job started", "job-123", "researcher", "Worker tier: `premium`", "Use `/job job-123`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
}

func TestFormatRoleJobFinalSuccessIncludesSummaryAndArtifactHints(t *testing.T) {
	out := formatRoleJobFinal(storage.Job{
		JobID:    "job-123",
		Status:   "succeeded",
		RoleName: "researcher",
		Worker:   "premium",
		Summary:  "found the answer",
	}, []artifactview.Info{{
		ID:    7,
		Label: "Screenshot",
		Type:  "screenshot",
		Path:  "/tmp/ok-gobot-safe/shot.png",
		Display: artifactview.DisplayMetadata{
			Kind: artifactview.KindImage,
			Safe: true,
		},
	}})

	for _, want := range []string{"Role job completed", "Summary:", "found the answer", "Proof artifacts:", "Screenshot (#7): safe local image artifact", "Use `/job job-123`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
	if strings.Contains(out, "/tmp/ok-gobot-safe") {
		t.Fatalf("final notification leaked a local path: %q", out)
	}
}

func TestFormatRoleJobFinalHiddenArtifactDoesNotLeakLocalPath(t *testing.T) {
	unsafePath := "/tmp/ok-gobot-secret/proof.png"
	out := formatRoleJobFinal(storage.Job{
		JobID:   "job-unsafe",
		Status:  "succeeded",
		Summary: "done",
	}, []artifactview.Info{{
		ID:    8,
		Label: "Secret screenshot",
		Type:  "screenshot",
		Path:  unsafePath,
		Display: artifactview.DisplayMetadata{
			Kind:   artifactview.KindImage,
			Safe:   false,
			Reason: "artifact file not found",
		},
	}})

	for _, want := range []string{"Proof artifacts:", "Secret screenshot (#8): hidden (artifact file not found)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
	if strings.Contains(out, unsafePath) || strings.Contains(out, "/tmp/ok-gobot-secret") {
		t.Fatalf("final notification leaked an unsafe local path: %q", out)
	}
}

func TestFormatRoleJobFinalFailureIncludesReason(t *testing.T) {
	out := formatRoleJobFinal(storage.Job{
		JobID:  "job-err",
		Status: "timed_out",
		Error:  "context deadline exceeded",
	}, nil)
	for _, want := range []string{"Role job timed out", "Reason: context deadline exceeded", "Use `/job job-err`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
}

func TestFormatRoleJobFinalPreflightFailureUsesNormalizedReason(t *testing.T) {
	const summary = "preflight failed: [github.auth] GitHub authentication is missing or invalid. Hint: Run gh auth login and ensure the token can create PRs, checks, and reviews."
	out := formatRoleJobFinal(storage.Job{
		JobID:  "job-preflight-tg",
		Status: "preflight_failed",
		Error:  summary,
	}, nil)
	for _, want := range []string{"Role job blocked by preflight", "Reason: " + summary, "Use `/job job-preflight-tg`"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
	if strings.Count(out, "preflight failed") != 1 {
		t.Fatalf("Telegram output repeated preflight headline: %q", out)
	}
}

func TestSendRoleJobFinalNotificationRetriesPlainTextWhenMarkdownFails(t *testing.T) {
	tg := newFakeTelegramAPI(t)
	tg.failNextMarkdownSend()

	api, err := telebot.NewBot(telebot.Settings{
		Token:   "TEST",
		URL:     tg.server.URL,
		Client:  tg.server.Client(),
		Offline: true,
	})
	if err != nil {
		t.Fatalf("telebot.NewBot() error = %v", err)
	}
	api.Me = &telebot.User{ID: 1, Username: "okgobot", IsBot: true}

	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close() //nolint:errcheck

	bot := &Bot{api: api, store: store}
	bot.sendRoleJobFinalNotification(&telebot.Chat{ID: 99, Type: telebot.ChatPrivate}, storage.Job{
		JobID:    "job-md",
		Status:   "succeeded",
		RoleName: "researcher",
		Worker:   "premium",
		Summary:  "AI produced *unbalanced _markdown [tokens",
	})

	var sends []telegramRequest
	for _, req := range tg.snapshotRequests() {
		if req.Method == "sendMessage" {
			sends = append(sends, req)
		}
	}
	if len(sends) != 2 {
		t.Fatalf("sendMessage request count = %d, want 2: %+v", len(sends), sends)
	}
	if sends[0].ParseMode != telebot.ModeMarkdown {
		t.Fatalf("first send parse mode = %q, want Markdown", sends[0].ParseMode)
	}
	if sends[1].ParseMode != "" {
		t.Fatalf("fallback send parse mode = %q, want plain text", sends[1].ParseMode)
	}
	if !strings.Contains(sends[1].Text, "AI produced *unbalanced _markdown [tokens") {
		t.Fatalf("fallback send lost summary text: %q", sends[1].Text)
	}
}
