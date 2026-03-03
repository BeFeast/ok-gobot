package bot

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/config"
	"ok-gobot/internal/storage"
)

type fakeContext struct {
	telebot.Context
	msg  *telebot.Message
	sent []string
}

func (c *fakeContext) Message() *telebot.Message {
	return c.msg
}

func (c *fakeContext) Chat() *telebot.Chat {
	return c.msg.Chat
}

func (c *fakeContext) Sender() *telebot.User {
	return c.msg.Sender
}

func (c *fakeContext) Send(what interface{}, _ ...interface{}) error {
	c.sent = append(c.sent, fmt.Sprint(what))
	return nil
}

func TestHandleMessage_DeniesUnauthorizedDirectMessageWithoutSideEffects(t *testing.T) {
	bot, store, memoryRoot := newUnauthorizedDMTestBot(t)

	ctx := &fakeContext{
		msg: &telebot.Message{
			ID:   101,
			Text: "hello from an intruder",
			Chat: &telebot.Chat{ID: 7001, Type: telebot.ChatPrivate},
			Sender: &telebot.User{
				ID:       9001,
				Username: "intruder",
			},
		},
	}

	logs := captureLogs(t, func() {
		if err := bot.handleMessage(context.Background(), ctx); err != nil {
			t.Fatalf("handleMessage() error = %v", err)
		}
	})

	if len(ctx.sent) != 1 || ctx.sent[0] != unauthorizedDMMessage {
		t.Fatalf("unexpected responses: %#v", ctx.sent)
	}

	assertTableCount(t, store, "messages", 0)
	assertTableCount(t, store, "sessions", 0)
	assertTableCount(t, store, "session_messages", 0)

	if _, err := os.Stat(filepath.Join(memoryRoot, "memory")); !os.IsNotExist(err) {
		t.Fatalf("expected no memory writes, stat err = %v", err)
	}

	if !strings.Contains(logs, "audit=telegram_dm_auth_deny") {
		t.Fatalf("expected audit log, got %q", logs)
	}
	for _, want := range []string{
		"sender_id=9001",
		"chat_id=7001",
		"channel=telegram_dm",
		`username="intruder"`,
		"reason=unauthorized_dm_sender",
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("expected %q in logs: %q", want, logs)
		}
	}
	if !regexp.MustCompile(`timestamp=\d{4}-\d{2}-\d{2}T`).MatchString(logs) {
		t.Fatalf("expected RFC3339 timestamp in logs: %q", logs)
	}
}

func TestGuardUnauthorizedDM_BlocksWrappedHandlerBeforeStateMutation(t *testing.T) {
	bot, store, _ := newUnauthorizedDMTestBot(t)
	const chatID = 7002

	if err := store.SaveSession(chatID, "existing shared session"); err != nil {
		t.Fatalf("SaveSession() error = %v", err)
	}

	called := false
	handler := bot.guardUnauthorizedDM(false, func(c telebot.Context) error {
		called = true
		return store.SaveSession(c.Chat().ID, "")
	})

	ctx := &fakeContext{
		msg: &telebot.Message{
			ID:   102,
			Text: "/clear",
			Chat: &telebot.Chat{ID: chatID, Type: telebot.ChatPrivate},
			Sender: &telebot.User{
				ID:       9002,
				Username: "blocked_user",
			},
		},
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("guarded handler error = %v", err)
	}

	if called {
		t.Fatal("expected wrapped handler not to be called")
	}
	if len(ctx.sent) != 1 || ctx.sent[0] != unauthorizedDMMessage {
		t.Fatalf("unexpected responses: %#v", ctx.sent)
	}

	session, err := store.GetSession(chatID)
	if err != nil {
		t.Fatalf("GetSession() error = %v", err)
	}
	if session != "existing shared session" {
		t.Fatalf("session mutated on deny: %q", session)
	}
}

func TestGuardUnauthorizedDM_AllowsExplicitlyUnauthenticatedPairingCommand(t *testing.T) {
	bot, _, _ := newUnauthorizedDMTestBot(t)

	called := false
	handler := bot.guardUnauthorizedDM(true, func(c telebot.Context) error {
		called = true
		return c.Send("paired")
	})

	ctx := &fakeContext{
		msg: &telebot.Message{
			ID:   103,
			Text: "/pair 123456",
			Chat: &telebot.Chat{ID: 7003, Type: telebot.ChatPrivate},
			Sender: &telebot.User{
				ID:       9003,
				Username: "new_user",
			},
		},
	}

	if err := handler(ctx); err != nil {
		t.Fatalf("guarded handler error = %v", err)
	}

	if !called {
		t.Fatal("expected wrapped pairing handler to be called")
	}
	if len(ctx.sent) != 1 || ctx.sent[0] != "paired" {
		t.Fatalf("unexpected responses: %#v", ctx.sent)
	}
}

func newUnauthorizedDMTestBot(t *testing.T) (*Bot, *storage.Store, string) {
	t.Helper()

	root := t.TempDir()
	store, err := storage.New(filepath.Join(root, "bot.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("store.Close() error = %v", err)
		}
	})

	bot := &Bot{
		store:  store,
		memory: agent.NewMemory(root),
		authManager: &AuthManager{
			store:        store,
			config:       config.AuthConfig{Mode: "allowlist"},
			pairingCodes: make(map[string]*PairingCode),
		},
		groupManager: NewGroupManager(store, "active", "okgobot"),
		safety:       agent.NewSafety(),
		rateLimiter:  NewRateLimiter(10, time.Minute),
	}

	return bot, store, root
}

func assertTableCount(t *testing.T, store *storage.Store, table string, want int) {
	t.Helper()

	var got int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("table %s count = %d, want %d", table, got, want)
	}
}

func captureLogs(t *testing.T, fn func()) string {
	t.Helper()

	var buf bytes.Buffer
	origWriter := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	})

	fn()
	return buf.String()
}
