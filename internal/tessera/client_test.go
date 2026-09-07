package tessera

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	file := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(file, []byte("synthetic-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return Config{Enabled: true, Endpoint: "127.0.0.1:12345", TokenFile: file, ConnectorID: "connector", Workspace: Workspace{"00000000-0000-4000-8000-000000000134", "/fixture/brain", "records", true}, InstanceID: "instance", AccountID: "bot", ActorID: "operator", SenderID: "123", Routes: []Route{{ChatID: "123"}}}
}
func testTelegram() Telegram {
	return Telegram{SenderID: "123", ChatID: "123", MessageID: "456", UpdateID: "789"}
}
func TestSharedPolicyFixture(t *testing.T) {
	b, err := os.ReadFile("testdata/connector-policy-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures struct {
		Fixtures []struct {
			Name        string
			Config      Config
			Fingerprint string `json:"policy_fingerprint"`
		}
	}
	if err = json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures.Fixtures {
		t.Run(f.Name, func(t *testing.T) {
			if got := f.Config.PolicyFingerprint(); got != f.Fingerprint {
				t.Fatalf("%s != %s", got, f.Fingerprint)
			}
		})
	}
}
func TestImmutableTurnAndConfig(t *testing.T) {
	c := testConfig(t)
	topic := "7"
	c.Routes[0].TopicID = &topic
	client, err := NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	topic = "9"
	if *client.config.Routes[0].TopicID != "7" {
		t.Fatal("mutable config")
	}
	turn := &Turn{Telegram: testTelegram(), Content: "original"}
	turn.Telegram.TopicID = &topic
	ctx := WithTurn(context.Background(), turn)
	topic = "10"
	turn.Content = "new"
	got, ok := TrustedTurn(ctx)
	if !ok || got.Content != "original" || *got.Telegram.TopicID != "9" {
		t.Fatal("mutable turn")
	}
	*got.Telegram.TopicID = "11"
	next, _ := TrustedTurn(ctx)
	if *next.Telegram.TopicID != "9" {
		t.Fatal("mutable returned turn")
	}
	if _, ok = TrustedTurn(WithTurn(ctx, nil)); ok {
		t.Fatal("trust not cleared")
	}
	fingerprint := c.Fingerprint()
	policy := c.PolicyFingerprint()
	c.TokenFile = "rotated"
	if c.Fingerprint() != fingerprint || c.PolicyFingerprint() != policy {
		t.Fatal("rotation changed binding")
	}
	c.AccountID = "changed"
	if c.Fingerprint() == fingerprint || c.PolicyFingerprint() == policy {
		t.Fatal("account drift ignored")
	}
}
func TestClientEnvelopeAndFailurePaths(t *testing.T) {
	for _, mode := range []string{"success", "stale", "policy", "wrong-id", "wrong-schema", "partial"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			cfg := testConfig(t)
			cfg.Endpoint = listener.Addr().String()
			client, _ := NewClient(cfg)
			observed := make(chan map[string]any, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadBytes('\n')
				var req map[string]any
				_ = json.Unmarshal(line, &req)
				observed <- req
				if mode == "partial" {
					fmt.Fprint(conn, `{"schema":`)
					return
				}
				reply := map[string]any{"schema": Schema, "id": req["id"], "ok": true, "data": map[string]any{"value": "ok"}}
				switch mode {
				case "wrong-id":
					reply["id"] = "other"
				case "wrong-schema":
					reply["schema"] = "ai-brain/native"
				case "stale", "policy":
					reply["ok"] = false
					code := "attention_stale"
					if mode == "policy" {
						code = "connector_policy_mismatch"
					}
					reply["error"] = map[string]any{"code": code, "message": "fixture rejection", "current": nil}
				}
				_ = json.NewEncoder(conn).Encode(reply)
			}()
			_, err = client.Call(context.Background(), Intent{Telegram: testTelegram(), Command: map[string]any{"op": "attention_ack", "operation_id": "00000000-0000-4000-8000-000000000001", "goal_id": "g", "attention_id": "a", "expected_revision": "r", "stage_id": nil}})
			if (mode == "success") != (err == nil) {
				t.Fatalf("mode=%s error=%v", mode, err)
			}
			req := <-observed
			connector := req["connector"].(map[string]any)
			if connector["token"] != "synthetic-token" || connector["policy_fingerprint"] != cfg.PolicyFingerprint() {
				t.Fatal("incorrect credential/policy envelope")
			}
			command := req["command"].(map[string]any)
			if v, ok := command["stage_id"]; !ok || v != nil {
				t.Fatal("lost explicit null")
			}
			if _, ok := command["source"]; ok {
				t.Fatal("source leaked")
			}
		})
	}
}
func TestClientRejectsBeforeTransport(t *testing.T) {
	c := testConfig(t)
	c.TokenFile = "missing"
	client, _ := NewClient(c)
	for _, command := range []map[string]any{{"op": "start_stage"}, {"op": "inbox_capture", "source": map[string]any{}}, {"op": "attention_ack"}, {"op": "inbox_list", "actor_id": "forged"}} {
		_, err := client.Call(context.Background(), Intent{Telegram: testTelegram(), Command: command})
		if err == nil || strings.Contains(err.Error(), "credential") {
			t.Fatalf("not rejected before credential/transport: %v", err)
		}
	}
	bad := testTelegram()
	bad.SenderID = "999"
	if _, err := client.Call(context.Background(), Intent{Telegram: bad, Command: map[string]any{"op": "inbox_list"}}); err == nil || strings.Contains(err.Error(), "credential") {
		t.Fatal("wrong sender reached transport")
	}
}
