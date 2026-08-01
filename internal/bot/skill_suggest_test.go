package bot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

func TestSkillSuggestCommand_AdminCreatesConciseDraftHint(t *testing.T) {
	bot, soul, store := newSkillSuggestCommandTestBot(t, 101)
	if err := store.CreateJob(storage.Job{
		JobID:       "job-tg-success",
		Kind:        "role",
		Status:      "succeeded",
		Description: "role:researcher",
		RoleName:    "researcher",
		Summary:     "Telegram draft summary.",
	}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	ctx := newSkillSuggestCommandContext("job-tg-success", 101, "admin")
	if err := bot.handleSkillSuggestCommand(ctx); err != nil {
		t.Fatalf("handleSkillSuggestCommand: %v", err)
	}
	assertSentContains(t, ctx, "Skill draft saved")
	assertSentContains(t, ctx, "Audit: passed")
	assertSentContains(t, ctx, "Next: review it")
	assertSentContains(t, ctx, "install only after approval")
	if strings.Contains(strings.Join(ctx.sent, "\n"), soul) {
		t.Fatalf("Telegram response should not leak absolute soul path: %#v", ctx.sent)
	}
	if _, err := os.Stat(filepath.Join(soul, "skills")); !os.IsNotExist(err) {
		t.Fatalf("skill_suggest must not install skills: %v", err)
	}
}

func TestSkillSuggestCommand_NonAdminCannotSuggest(t *testing.T) {
	bot, _, _ := newSkillSuggestCommandTestBot(t, 101)

	ctx := newSkillSuggestCommandContext("job-tg-success", 202, "notadmin")
	if err := bot.handleSkillSuggestCommand(ctx); err != nil {
		t.Fatalf("handleSkillSuggestCommand: %v", err)
	}
	assertSentContains(t, ctx, "admin-only")
}

func TestSkillSuggestCommand_ReportsInvalidJobState(t *testing.T) {
	bot, _, store := newSkillSuggestCommandTestBot(t, 101)
	if err := store.CreateJob(storage.Job{JobID: "job-tg-running", Kind: "role", Status: "running"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	ctx := newSkillSuggestCommandContext("job-tg-running", 101, "admin")
	if err := bot.handleSkillSuggestCommand(ctx); err != nil {
		t.Fatalf("handleSkillSuggestCommand: %v", err)
	}
	assertSentContains(t, ctx, "require succeeded jobs")
}

func newSkillSuggestCommandTestBot(t *testing.T, adminID int64) (*Bot, string, *storage.Store) {
	t.Helper()
	soul := t.TempDir()
	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	bot := &Bot{
		store:  store,
		memory: agent.NewMemory(soul),
		authManager: &AuthManager{
			config: config.AuthConfig{AdminID: adminID},
		},
	}
	return bot, soul, store
}

func newSkillSuggestCommandContext(payload string, senderID int64, username string) *fakeContext {
	return &fakeContext{
		msg: &telebot.Message{
			Payload: payload,
			Chat:    &telebot.Chat{ID: senderID, Type: telebot.ChatPrivate},
			Sender:  &telebot.User{ID: senderID, Username: username},
		},
	}
}
