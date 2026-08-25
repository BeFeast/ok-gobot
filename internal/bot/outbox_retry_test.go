package bot

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"ok-gobot/internal/storage"

	"gopkg.in/telebot.v4"
)

// The failure being pinned: an answer that was computed, committed, but never
// sent — because the send failed or the process died first. On the next start
// it must actually arrive, and it must not arrive twice.

func newOutboxTestBot(t *testing.T, server *httptest.Server) (*Bot, *storage.Store) {
	t.Helper()
	api, err := telebot.NewBot(telebot.Settings{
		Offline: true,
		Token:   "token",
		URL:     server.URL,
		Client:  server.Client(),
	})
	if err != nil {
		t.Fatalf("telebot.NewBot: %v", err)
	}
	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &Bot{api: api, store: store}, store
}

func TestOutboxRedeliversAfterRestart(t *testing.T) {
	var sends int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sends++
		_, _ = fmt.Fprint(w, `{"ok":true,"result":{"message_id":99,"chat":{"id":7,"type":"private"}}}`)
	}))
	defer server.Close()

	b, store := newOutboxTestBot(t, server)

	// A previous process committed the answer and died before sending it.
	if _, err := store.EnqueueOutbox(7, "the answer nobody saw", "task"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	b.drainOutbox()
	if sends != 1 {
		t.Fatalf("expected exactly 1 send on restart, got %d", sends)
	}

	// A second pass must not send it again.
	b.drainOutbox()
	if sends != 1 {
		t.Fatalf("answer was re-sent: %d sends total", sends)
	}

	pending, err := store.PendingOutbox(10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected nothing owed after redelivery, got %d", len(pending))
	}
}

func TestOutboxKeepsOwingWhenTelegramRefuses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	}))
	defer server.Close()

	b, store := newOutboxTestBot(t, server)
	if _, err := store.EnqueueOutbox(7, "undeliverable", "task"); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	b.drainOutbox()

	pending, err := store.PendingOutbox(10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("a failed send must stay owed, got %d pending", len(pending))
	}
	if pending[0].Attempts != 1 {
		t.Fatalf("attempt not counted: %d", pending[0].Attempts)
	}
	if pending[0].LastError == "" {
		t.Fatal("failure reason not recorded")
	}
}
