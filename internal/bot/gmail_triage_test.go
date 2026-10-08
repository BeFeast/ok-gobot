package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/telebot.v4"

	"ok-gobot/internal/config"
	"ok-gobot/internal/gmailtriage"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tools"
)

const triageOwnerChat = int64(4242)

type triageCall struct {
	Method      string
	ChatID      string
	MessageID   string
	Text        string
	ReplyMarkup string
	ID          int // message_id returned by the fake
}

// triageTelegram fakes the Bot API and keeps reply markup for button tests.
type triageTelegram struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []triageCall
	nextID int
}

func newTriageTelegram(t *testing.T) *triageTelegram {
	f := &triageTelegram{nextID: 500}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw map[string]any
		_ = json.NewDecoder(r.Body).Decode(&raw)
		str := func(k string) string {
			switch v := raw[k].(type) {
			case string:
				return v
			case nil:
				return ""
			default:
				b, _ := json.Marshal(v)
				return string(b)
			}
		}
		call := triageCall{Method: path.Base(r.URL.Path), ChatID: str("chat_id"), MessageID: str("message_id"), Text: str("text"), ReplyMarkup: str("reply_markup")}
		f.mu.Lock()
		f.nextID++
		id := f.nextID
		call.ID = id
		f.calls = append(f.calls, call)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch call.Method {
		case "sendMessage", "editMessageText":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{
				"message_id": id, "date": 0, "chat": map[string]any{"id": triageOwnerChat, "type": "private"}, "text": call.Text,
			}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *triageTelegram) sent() []triageCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []triageCall
	for _, c := range f.calls {
		if c.Method == "sendMessage" {
			out = append(out, c)
		}
	}
	return out
}

func (f *triageTelegram) all() []triageCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]triageCall(nil), f.calls...)
}

// callbackData pulls "op|arg" payloads of the "gt" endpoint from reply markup.
func callbackData(markup string) []string {
	var kb struct {
		InlineKeyboard [][]struct {
			Text string `json:"text"`
			Data string `json:"callback_data"`
		} `json:"inline_keyboard"`
	}
	_ = json.Unmarshal([]byte(markup), &kb)
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, b := range row {
			out = append(out, strings.TrimPrefix(b.Data, "\fgt|"))
		}
	}
	return out
}

func findData(data []string, prefix string) string {
	for _, d := range data {
		if strings.HasPrefix(d, prefix) {
			return d
		}
	}
	return ""
}

// triageMailbox is a minimal in-memory mailbox for the Telegram layer.
type triageMailbox struct {
	mu      sync.Mutex
	thread  gmailtriage.Thread
	sent    []gmailtriage.Reply
	trashed []string
}

func (m *triageMailbox) Search(context.Context, string, int) ([]string, error) {
	return []string{m.thread.ID}, nil
}
func (m *triageMailbox) Thread(context.Context, string) (gmailtriage.Thread, error) {
	return m.thread, nil
}
func (m *triageMailbox) EnsureLabels(context.Context, []string) error { return nil }
func (m *triageMailbox) ModifyThread(context.Context, string, []string, []string) error {
	return nil
}
func (m *triageMailbox) TrashThread(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trashed = append(m.trashed, id)
	return nil
}
func (m *triageMailbox) SendReply(_ context.Context, r gmailtriage.Reply) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, r)
	return nil
}

func newTriageTestBot(t *testing.T, mode string) (*Bot, *triageTelegram, *triageMailbox, gmailtriage.Profile) {
	t.Helper()
	tg := newTriageTelegram(t)
	api, err := telebot.NewBot(telebot.Settings{Token: "TEST", URL: tg.server.URL, Client: tg.server.Client(), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	api.Me = &telebot.User{ID: 777, Username: "testbot", IsBot: true}
	store, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	mb := &triageMailbox{thread: gmailtriage.Thread{ID: "t1", Messages: []gmailtriage.Message{{
		ID: "m1", ThreadID: "t1", From: "Dana <dana@acme.test>", Subject: "Contract <draft>", Body: "Can you confirm by Friday?",
		Labels: []string{"INBOX"}, Time: time.Now().Add(-time.Hour),
	}}}}
	llm := gmailtriage.CompleterFunc(func(context.Context, string, string) (string, error) {
		return `[{"id":"t1","bucket":"reply","why":"asks to confirm","draft":"Confirmed."}]`, nil
	})
	gstore, err := gmailtriage.NewStore(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	svc, err := gmailtriage.New(gmailtriage.Config{Enabled: true, Profiles: []gmailtriage.ProfileConfig{{
		Name: "personal", Mode: mode, Taxonomy: "owner", Language: "en", Account: "owner@example.com", ChatID: triageOwnerChat,
	}}}, gmailtriage.Options{Store: gstore, Completer: llm, Mailboxes: func(gmailtriage.Profile) (gmailtriage.Reader, gmailtriage.Writer) { return mb, mb }})
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{
		api:          api,
		store:        store,
		authManager:  NewAuthManager(store, config.AuthConfig{Mode: "open"}),
		toolRegistry: tools.NewRegistry(),
	}
	b.SetGmailTriage(svc)
	return b, tg, mb, svc.Profiles()[0]
}

func pressButton(t *testing.T, b *Bot, data string, cardID int, senderID int64) {
	t.Helper()
	c := b.api.NewContext(telebot.Update{Callback: &telebot.Callback{
		ID:      "cb",
		Sender:  &telebot.User{ID: senderID},
		Data:    data,
		Unique:  gmailTriageCallback,
		Message: &telebot.Message{ID: cardID, Chat: &telebot.Chat{ID: triageOwnerChat, Type: telebot.ChatPrivate}},
	}})
	if err := b.handleGmailTriageCallback(c); err != nil {
		t.Fatalf("callback %q: %v", data, err)
	}
}

func TestGmailTriageDigestButtonsSendOnlyAfterPress(t *testing.T) {
	b, tg, mb, p := newTriageTestBot(t, "assistant")

	if summary, err := b.runGmailTriage(context.Background(), p, true); err != nil {
		t.Fatalf("run: %v (%s)", err, summary)
	}
	sent := tg.sent()
	if len(sent) != 2 {
		t.Fatalf("expected header + one card, got %+v", sent)
	}
	card := sent[1]
	if !strings.Contains(card.Text, "Contract &lt;draft&gt;") || !strings.Contains(card.Text, "Confirmed.") {
		t.Fatalf("card text not escaped or missing draft: %q", card.Text)
	}
	data := callbackData(card.ReplyMarkup)
	send := findData(data, "a|")
	if send == "" || findData(data, "e|") == "" || findData(data, "b|") == "" {
		t.Fatalf("card buttons = %v", data)
	}
	if len(mb.sent) != 0 {
		t.Fatal("mail sent before any button press")
	}

	cardID := card.ID

	// Someone else pressing the button in the owner's chat does nothing.
	pressButton(t, b, send, cardID, 9999)
	if len(mb.sent) != 0 {
		t.Fatal("a foreign sender's press sent mail")
	}
	pressButton(t, b, send, cardID, triageOwnerChat)
	if len(mb.sent) != 1 || mb.sent[0].Body != "Confirmed." || mb.sent[0].To != "dana@acme.test" {
		t.Fatalf("sent = %+v", mb.sent)
	}
	// A second press of the same button does not send again.
	pressButton(t, b, send, cardID, triageOwnerChat)
	if len(mb.sent) != 1 {
		t.Fatalf("double press sent %d replies", len(mb.sent))
	}
	var edited bool
	for _, c := range tg.all() {
		if c.Method == "editMessageText" && strings.Contains(c.Text, "Sent.") {
			edited = true
		}
	}
	if !edited {
		t.Fatal("card was not updated after sending")
	}
}

func TestGmailTriageEditDraftThenSend(t *testing.T) {
	b, tg, mb, p := newTriageTestBot(t, "assistant")
	if _, err := b.runGmailTriage(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	card := tg.sent()[1]
	data := callbackData(card.ReplyMarkup)
	oldSend := findData(data, "a|")
	pressButton(t, b, findData(data, "e|"), card.ID, triageOwnerChat)

	all := tg.sent()
	prompt := all[len(all)-1]
	if !strings.Contains(prompt.Text, "new draft") || !strings.Contains(prompt.ReplyMarkup, "force_reply") {
		t.Fatalf("edit prompt = %+v", prompt)
	}
	promptID := prompt.ID
	reply := &fakeContext{msg: &telebot.Message{
		Text:    "Confirmed, see you Friday.",
		Chat:    &telebot.Chat{ID: triageOwnerChat, Type: telebot.ChatPrivate},
		Sender:  &telebot.User{ID: triageOwnerChat},
		ReplyTo: &telebot.Message{ID: promptID, Sender: &telebot.User{ID: 777}},
	}}
	handled, err := b.handleGmailTriageReply(context.Background(), reply)
	if err != nil || !handled {
		t.Fatalf("reply handled=%v err=%v", handled, err)
	}
	all = tg.sent()
	newCard := all[len(all)-1]
	if !strings.Contains(newCard.Text, "Confirmed, see you Friday.") {
		t.Fatalf("new card = %q", newCard.Text)
	}
	// The old Send button no longer works; the new one sends the new text.
	pressButton(t, b, oldSend, card.ID, triageOwnerChat)
	if len(mb.sent) != 0 {
		t.Fatal("stale Send button sent the old draft")
	}
	pressButton(t, b, findData(callbackData(newCard.ReplyMarkup), "a|"), newCard.ID, triageOwnerChat)
	if len(mb.sent) != 1 || mb.sent[0].Body != "Confirmed, see you Friday." {
		t.Fatalf("sent = %+v", mb.sent)
	}
}

func TestGmailTriageReadOnlyCardHasNoMailActions(t *testing.T) {
	b, tg, mb, p := newTriageTestBot(t, "read_only")
	if _, err := b.runGmailTriage(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	card := tg.sent()[1]
	data := callbackData(card.ReplyMarkup)
	if findData(data, "a|") != "" || findData(data, "e|") != "" {
		t.Fatalf("read-only card offers mail actions: %v", data)
	}
	if strings.Contains(card.Text, "Confirmed.") {
		t.Fatal("read-only card shows a send draft")
	}
	// Re-bucket flow still works and changes nothing in the mailbox.
	pressButton(t, b, findData(data, "b|"), card.ID, triageOwnerChat)
	pressButton(t, b, "r|1|fyi|s", card.ID, triageOwnerChat)
	if len(mb.sent)+len(mb.trashed) != 0 {
		t.Fatal("read-only correction touched the mailbox")
	}
}

func TestGmailTriageScheduledEmptyDigestStaysSilent(t *testing.T) {
	b, tg, _, p := newTriageTestBot(t, "assistant")
	if _, err := b.runGmailTriage(context.Background(), p, true); err != nil {
		t.Fatal(err)
	}
	before := len(tg.sent())
	if _, err := b.runGmailTriage(context.Background(), p, false); err != nil {
		t.Fatal(err)
	}
	if len(tg.sent()) != before {
		t.Fatalf("scheduled run with nothing new sent %d messages", len(tg.sent())-before)
	}
}

func TestGmailTriageMenuAndTool(t *testing.T) {
	b, _, _, _ := newTriageTestBot(t, "read_only")
	cmds := b.builtinCommands()
	if cmds[len(cmds)-1].Text != "triage" {
		t.Fatalf("triage missing from menu: %v", cmds[len(cmds)-1])
	}
	tool, ok := b.toolRegistry.Get("gmail_triage")
	if !ok {
		t.Fatal("gmail_triage tool not registered")
	}
	cs, ok := tools.AsChatScoped(tool)
	if !ok {
		t.Fatal("gmail_triage is not chat scoped")
	}
	bound := cs.BindChat(nil, triageOwnerChat).(*tools.GmailTriageTool)
	out, err := bound.ExecuteJSON(context.Background(), map[string]string{"action": "schedule", "op": "set", "times": "10:00,19:00", "days": "sun-thu"})
	if err != nil || !strings.Contains(out, "10:00, 19:00") {
		t.Fatalf("schedule via tool = %q, %v", out, err)
	}
	other := cs.BindChat(nil, 1).(*tools.GmailTriageTool)
	if _, err := other.ExecuteJSON(context.Background(), map[string]string{"action": "status"}); err == nil {
		t.Fatal("tool served a chat without a profile")
	}
}

func TestGmailTriageQuietListPages(t *testing.T) {
	b, tg, _, p := newTriageTestBot(t, "assistant")
	store := b.gmailTriage.Store()
	var lo, hi int64
	for i := 0; i < 20; i++ {
		it, err := store.UpsertItem(gmailtriage.Item{
			Profile: p.Name, ThreadID: fmt.Sprintf("n%02d", i), LastMessageID: fmt.Sprintf("m%02d", i),
			Sender: "news@shop.test", SenderName: "Shop", Subject: strings.Repeat("Sale ", 20), Bucket: gmailtriage.BucketBulk,
			Why: strings.Repeat("newsletter ", 10), MessageTime: time.Now().Add(-time.Duration(i) * time.Minute),
		})
		if err != nil {
			t.Fatal(err)
		}
		if lo == 0 {
			lo = it.ID
		}
		hi = it.ID
	}
	pressButton(t, b, fmt.Sprintf("q|%d|%d", lo, hi), 1, triageOwnerChat)
	page := tg.sent()[len(tg.sent())-1]
	data := callbackData(page.ReplyMarkup)
	if len([]rune(page.Text)) > 4096 || strings.Count(page.Text, "\n   ") != 15 {
		t.Fatalf("first page: %d chars, %d items", len([]rune(page.Text)), strings.Count(page.Text, "\n   "))
	}
	more := findData(data, "q|")
	if more == "" {
		t.Fatalf("no More button: %v", data)
	}
	pressButton(t, b, more, 1, triageOwnerChat)
	page2 := tg.sent()[len(tg.sent())-1]
	if strings.Count(page2.Text, "\n   ") != 5 || !strings.Contains(page2.Text, "16. ") || findData(callbackData(page2.ReplyMarkup), "q|") != "" {
		t.Fatalf("second page = %q", page2.Text)
	}
}
