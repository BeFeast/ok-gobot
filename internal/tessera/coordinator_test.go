package tessera

import (
	"context"
	"encoding/json"
	"errors"
	"ok-gobot/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTransport struct {
	calls []Intent
	fail  error
}

func (f *fakeTransport) Call(_ context.Context, p Intent) (json.RawMessage, error) {
	f.calls = append(f.calls, p)
	if f.fail != nil {
		return nil, f.fail
	}
	return json.Marshal(map[string]any{"receipt": Receipt{OperationID: p.Command["operation_id"].(string), Status: "committed"}, "capture_id": "capture", "decision_id": "decision", "acknowledged_at": "time"})
}
func TestDurableLostResponseRestartAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	s, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t)
	transport := &fakeTransport{fail: errors.New("lost response")}
	c, _ := NewCoordinator(cfg, s, transport)
	tg := testTelegram()
	command := map[string]any{"op": "inbox_capture", "text": " exact\nbody "}
	if _, err = c.Mutate(context.Background(), tg, "command-capture", command); err == nil {
		t.Fatal("expected uncertainty")
	}
	first, _ := json.Marshal(transport.calls[0])
	if strings.Contains(string(first), "synthetic-token") {
		t.Fatal("secret persisted")
	}
	s.Close()
	s, err = storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	transport.fail = nil
	c, _ = NewCoordinator(cfg, s, transport)
	if _, err = c.Mutate(context.Background(), tg, "command-capture", command); err != nil {
		t.Fatal(err)
	}
	second, _ := json.Marshal(transport.calls[1])
	if string(first) != string(second) {
		t.Fatal("retry changed request")
	}
	if _, err = c.Mutate(context.Background(), tg, "command-capture", command); err != nil {
		t.Fatal(err)
	}
	if len(transport.calls) != 2 {
		t.Fatal("known receipt unnecessarily resent")
	}
	command["text"] = "changed"
	if _, err = c.Mutate(context.Background(), tg, "command-capture", command); err == nil {
		t.Fatal("changed update accepted")
	}
	command["text"] = " exact\nbody "
	cfg.AccountID = "changed"
	changed, _ := NewCoordinator(cfg, s, transport)
	if _, err = changed.Mutate(context.Background(), tg, "command-capture", command); err == nil {
		t.Fatal("config retargeted")
	}
	if len(transport.calls) != 2 {
		t.Fatal("conflict reached transport")
	}
	bytes, _ := os.ReadFile(path)
	if strings.Contains(string(bytes), "synthetic-token") {
		t.Fatal("secret in sqlite")
	}
}
func fixtureItem() Item {
	return Item{GoalID: "goal", AttentionID: "attention", Revision: "sha256:revision", GoalTitle: "fixture goal", Kind: "decision", Message: "Need input", Current: true, ActorID: "operator", Channel: "telegram", AllowedActions: []string{"save_decision", "ack_seen"}}
}
func TestBoundReplyExactStaleAndSeen(t *testing.T) {
	s, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := testConfig(t)
	transport := &fakeTransport{}
	c, _ := NewCoordinator(cfg, s, transport)
	tg := testTelegram()
	item := fixtureItem()
	id, err := c.EnqueueAttention(tg, item)
	if err != nil {
		t.Fatal(err)
	}
	same, err := c.EnqueueAttention(tg, item)
	if err != nil || same != id {
		t.Fatal("duplicate event")
	}
	if err = s.MarkOutboxDelivered(id, 0); err == nil {
		t.Fatal("unknown send accepted")
	}
	if _, err = c.BoundReply(context.Background(), tg, 88, "draft", false); !IsUnbound(err) {
		t.Fatalf("unknown mapping: %v", err)
	}
	if err = s.MarkOutboxDelivered(id, 88); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkOutboxDelivered(id, 99); err != nil {
		t.Fatal(err)
	}
	if _, err = c.BoundReply(context.Background(), tg, 99, "draft", false); !IsUnbound(err) {
		t.Fatal("second ack retargeted")
	}
	transport.fail = &APIError{Code: "attention_stale", Message: "old version", Current: json.RawMessage(`null`)}
	if _, err = c.BoundReply(context.Background(), tg, 88, "exact draft", false); err == nil {
		t.Fatal("stale succeeded")
	}
	if _, err = c.BoundReply(context.Background(), tg, 88, "exact draft", false); err == nil {
		t.Fatal("stale retry succeeded")
	}
	if len(transport.calls) != 1 {
		t.Fatal("known stale redelivered")
	}
	p := transport.calls[0]
	if p.Command["expected_revision"] != item.Revision || p.Command["goal_id"] != item.GoalID || p.Command["text"] != "exact draft" {
		t.Fatal("target changed")
	}
	b, _ := json.Marshal(p)
	if !strings.Contains(string(b), `"stage_id":null`) {
		t.Fatal("stage omission")
	}
	transport.fail = nil
	tg.UpdateID = "790"
	tg.MessageID = "457"
	if _, err = c.BoundReply(context.Background(), tg, 88, "/seen", true); err != nil {
		t.Fatal(err)
	}
	if transport.calls[1].Command["op"] != "attention_ack" {
		t.Fatal("seen did not ack")
	}
	wrong := tg
	wrong.SenderID = "999"
	if _, err = c.BoundReply(context.Background(), wrong, 88, "bad", false); err == nil {
		t.Fatal("wrong sender accepted")
	}
	topic := "7"
	wrong = tg
	wrong.TopicID = &topic
	if _, err = c.BoundReply(context.Background(), wrong, 88, "bad", false); err == nil {
		t.Fatal("wrong topic accepted")
	}
	if len(transport.calls) != 2 {
		t.Fatal("bad route reached transport")
	}
}
func TestToolReplayInterceptAndMalformedReceipt(t *testing.T) {
	s, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	transport := &fakeTransport{fail: errors.New("lost")}
	c, _ := NewCoordinator(testConfig(t), s, transport)
	tg := testTelegram()
	ctx := context.Background()
	if handled, err := c.ResumeTools(ctx, tg); handled || err != nil {
		t.Fatal("new update intercepted")
	}
	_, _ = c.Mutate(ctx, tg, "tool-inbox_capture", map[string]any{"op": "inbox_capture", "text": "original"})
	transport.fail = nil
	if handled, err := c.ResumeTools(ctx, tg); !handled || err != nil {
		t.Fatal("existing tool intent not recovered")
	}
	if transport.calls[0].Command["operation_id"] != transport.calls[1].Command["operation_id"] {
		t.Fatal("tool replay new op")
	}
}
