package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/telebot.v4"
	"ok-gobot/internal/tessera"
)

type tesseraTestTransport struct {
	calls []tessera.Intent
	fail  error
}

func (f *tesseraTestTransport) Call(_ context.Context, p tessera.Intent) (json.RawMessage, error) {
	f.calls = append(f.calls, p)
	if f.fail != nil {
		return nil, f.fail
	}
	if p.Command["op"] == "attention_list" {
		return json.RawMessage(`{"items":[],"complete":true}`), nil
	}
	return json.Marshal(map[string]any{"capture_id": "capture", "decision_id": "decision", "acknowledged_at": "time", "receipt": map[string]any{"operation_id": p.Command["operation_id"], "status": "committed"}})
}
func botTesseraConfig() tessera.Config {
	topic := "7"
	return tessera.Config{Enabled: true, Endpoint: "127.0.0.1:1", TokenFile: "/unused/synthetic", ConnectorID: "fixture", Workspace: tessera.Workspace{BrainID: "fixture", Root: "/fixture", RecordsDir: "records", Managed: true}, InstanceID: "instance", AccountID: "bot", ActorID: "operator", SenderID: "123", Routes: []tessera.Route{{ChatID: "123"}, {ChatID: "-456", TopicID: &topic}}}
}
func TestTesseraCommandsWithoutProviderAndExactBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99,"chat":{"id":123,"type":"private"}}}`)
	}))
	defer server.Close()
	b, s := newOutboxTestBot(t, server)
	b.api.Me = &telebot.User{ID: 900, IsBot: true}
	transport := &tesseraTestTransport{}
	var err error
	b.tessera, err = tessera.NewCoordinator(botTesseraConfig(), s, transport)
	if err != nil {
		t.Fatal(err)
	}
	message := &telebot.Message{ID: 44, Chat: &telebot.Chat{ID: 123, Type: telebot.ChatPrivate}, Sender: &telebot.User{ID: 123}, Text: "/capture  exact\nbody "}
	c := b.api.NewContext(telebot.Update{ID: 88, Message: message})
	if handled, err := b.handleTesseraMessage(context.Background(), c); !handled || err != nil {
		t.Fatal("capture not handled", err)
	}
	if b.ai != nil {
		t.Fatal("fixture unexpectedly has provider")
	}
	if len(transport.calls) != 1 || transport.calls[0].Command["text"] != " exact\nbody " {
		t.Fatal("capture body normalized")
	}
	if handled, err := b.handleTesseraMessage(context.Background(), c); !handled || err != nil {
		t.Fatal(err)
	}
	if len(transport.calls) != 1 {
		t.Fatal("duplicate update resent")
	}
	message = &telebot.Message{ID: 45, Chat: message.Chat, Sender: message.Sender, Text: "/attention"}
	if handled, err := b.handleTesseraMessage(context.Background(), b.api.NewContext(telebot.Update{ID: 89, Message: message})); !handled || err != nil {
		t.Fatal("attention needs provider", err)
	}
	message.Text = "draft that must not reach AI"
	message.ReplyTo = &telebot.Message{ID: 999, Sender: b.api.Me, Text: "Tessera · Attention\nunknown send"}
	before := len(transport.calls)
	if handled, err := b.handleTesseraMessage(context.Background(), b.api.NewContext(telebot.Update{ID: 90, Message: message})); !handled || err != nil {
		t.Fatal("unbound prompt fell through", err)
	}
	if len(transport.calls) != before {
		t.Fatal("unbound draft mutated")
	}
}
func TestTesseraOutboxForumFailureRecoveryAndBinding(t *testing.T) {
	sends := 0
	fail := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends++
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if fmt.Sprint(request["message_thread_id"]) != "7" {
			t.Errorf("topic lost: %v", request)
		}
		if !strings.Contains(fmt.Sprint(request["reply_markup"]), "force_reply") {
			t.Error("ForceReply missing")
		}
		if fail {
			fmt.Fprint(w, `{"ok":false,"error_code":500,"description":"fixture failure"}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"message_id":99,"message_thread_id":7,"chat":{"id":-456,"type":"supergroup"}}}`)
	}))
	defer server.Close()
	b, s := newOutboxTestBot(t, server)
	cfg := botTesseraConfig()
	transport := &tesseraTestTransport{}
	var err error
	b.tessera, err = tessera.NewCoordinator(cfg, s, transport)
	if err != nil {
		t.Fatal(err)
	}
	topic := "7"
	tg := tessera.Telegram{SenderID: "123", ChatID: "-456", TopicID: &topic}
	item := tessera.Item{GoalID: "g", AttentionID: "a", Revision: "sha256:r", GoalTitle: "goal", Kind: "decision", Message: "decide", Current: true, ActorID: "operator", Channel: "telegram", AllowedActions: []string{"save_decision", "ack_seen"}}
	if _, err = b.tessera.EnqueueAttention(tg, item); err != nil {
		t.Fatal(err)
	}
	b.drainOutbox()
	if _, err = s.TesseraReplyBinding(cfg.Fingerprint(), -456, 7, 123, 99); !tessera.IsUnbound(err) {
		t.Fatal("failed send invented binding")
	}
	fail = false
	b.drainOutbox()
	if _, err = s.TesseraReplyBinding(cfg.Fingerprint(), -456, 7, 123, 99); err != nil {
		t.Fatal(err)
	}
	b.drainOutbox()
	if sends != 2 {
		t.Fatal("wrong send count", sends)
	}
	item.Revision = "sha256:next"
	if _, err = b.tessera.EnqueueAttention(tg, item); err != nil {
		t.Fatal(err)
	}
	changed := cfg
	changed.AccountID = "different"
	b.tessera, _ = tessera.NewCoordinator(changed, s, transport)
	b.drainOutbox()
	if sends != 2 {
		t.Fatal("changed config sent old delivery")
	}
}
func TestTesseraTurnTrustClearedOnQueueCombinationAndMedia(t *testing.T) {
	turn := &tessera.Turn{Telegram: tessera.Telegram{SenderID: "123", ChatID: "123", MessageID: "44", UpdateID: "88"}, Content: "original"}
	ctx := tessera.WithTurn(context.Background(), turn)
	for i, tc := range []struct {
		d           telegramDelivery
		text        string
		media, want bool
	}{{telegramDelivery{Turn: turn}, "original", false, true}, {telegramDelivery{}, "queued", false, false}, {telegramDelivery{Turn: turn}, "original\nmerged", false, false}, {telegramDelivery{Turn: turn}, "original", true, false}} {
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			_, ok := tessera.TrustedTurn(tesseraRunContext(ctx, tc.d, tc.text, tc.media))
			if ok != tc.want {
				t.Fatal("wrong trusted context")
			}
		})
	}
}

func TestSetTesseraPinsActualBotAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("configuration used network") }))
	defer server.Close()
	b, _ := newOutboxTestBot(t, server)
	b.api.Me = &telebot.User{ID: 900, IsBot: true}
	cfg := botTesseraConfig()
	if err := b.SetTessera(cfg); err == nil {
		t.Fatal("wrong getMe account accepted")
	}
	cfg.AccountID = "900"
	if err := b.SetTessera(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestTesseraCommandForAnotherBotDoesNotCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected send") }))
	defer server.Close()
	b, s := newOutboxTestBot(t, server)
	b.api.Me = &telebot.User{ID: 900, Username: "our_bot", IsBot: true}
	transport := &tesseraTestTransport{}
	b.tessera, _ = tessera.NewCoordinator(botTesseraConfig(), s, transport)
	m := &telebot.Message{ID: 44, Chat: &telebot.Chat{ID: 123, Type: telebot.ChatPrivate}, Sender: &telebot.User{ID: 123}, Text: "/capture@another_bot private text"}
	if handled, err := b.handleTesseraMessage(context.Background(), b.api.NewContext(telebot.Update{ID: 88, Message: m})); handled || err != nil {
		t.Fatal("another bot command handled")
	}
	if len(transport.calls) != 0 {
		t.Fatal("another bot text captured")
	}
}
