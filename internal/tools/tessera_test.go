package tools

import (
	"context"
	"encoding/json"
	"ok-gobot/internal/storage"
	"ok-gobot/internal/tessera"
	"path/filepath"
	"testing"
)

type toolTesseraTransport struct{ calls []tessera.Intent }

func (f *toolTesseraTransport) Call(_ context.Context, p tessera.Intent) (json.RawMessage, error) {
	f.calls = append(f.calls, p)
	return json.Marshal(map[string]any{"capture_id": "capture", "receipt": map[string]any{"operation_id": p.Command["operation_id"], "status": "committed"}})
}
func TestTesseraToolRequiresTrustAndRejectsAuthorityArgs(t *testing.T) {
	s, err := storage.New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := tessera.Config{Enabled: true, Endpoint: "127.0.0.1:1", TokenFile: "/fixture", ConnectorID: "connector", Workspace: tessera.Workspace{BrainID: "brain", Root: "/brain", RecordsDir: "records", Managed: true}, InstanceID: "instance", AccountID: "bot", ActorID: "operator", SenderID: "123", Routes: []tessera.Route{{ChatID: "123"}}}
	transport := &toolTesseraTransport{}
	coordinator, err := tessera.NewCoordinator(cfg, s, transport)
	if err != nil {
		t.Fatal(err)
	}
	tool := &TesseraTool{Coordinator: coordinator, Op: "inbox_capture"}
	params := map[string]string{"text": "exact\nbody"}
	if _, err = tool.ExecuteJSON(context.Background(), params); err == nil {
		t.Fatal("background tool accepted")
	}
	ctx := tessera.WithTurn(context.Background(), &tessera.Turn{Telegram: tessera.Telegram{SenderID: "123", ChatID: "123", MessageID: "456", UpdateID: "789"}, Content: "capture this"})
	params["actor_id"] = "injected"
	if _, err = tool.ExecuteJSON(ctx, params); err == nil {
		t.Fatal("authority arg accepted")
	}
	delete(params, "actor_id")
	if _, err = tool.ExecuteJSON(ctx, params); err != nil {
		t.Fatal(err)
	}
	if _, err = tool.ExecuteJSON(ctx, params); err != nil {
		t.Fatal(err)
	}
	if len(transport.calls) != 1 || transport.calls[0].Command["text"] != "exact\nbody" {
		t.Fatal("tool normalized or duplicated capture")
	}
	params["text"] = "regenerated"
	if _, err = tool.ExecuteJSON(ctx, params); err == nil {
		t.Fatal("regenerated payload accepted")
	}
	if len(transport.calls) != 1 {
		t.Fatal("conflict reached transport")
	}
}
