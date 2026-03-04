package control_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	"ok-gobot/internal/control"
)

type mockTUIState struct {
	mu       sync.Mutex
	sessions []control.SessionInfo
	sent     []struct {
		chatID int64
		text   string
	}
	modelSet []struct {
		chatID int64
		model  string
	}
}

func (m *mockTUIState) GetStatus() map[string]interface{} {
	return map[string]interface{}{"ok": true}
}

func (m *mockTUIState) ListSessions() ([]control.SessionInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]control.SessionInfo, len(m.sessions))
	copy(out, m.sessions)
	return out, nil
}

func (m *mockTUIState) SendChat(chatID int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, struct {
		chatID int64
		text   string
	}{chatID: chatID, text: text})
	return nil
}

func (m *mockTUIState) AbortRun(_ int64) error                   { return nil }
func (m *mockTUIState) RespondToApproval(_ string, _ bool) error { return nil }
func (m *mockTUIState) SetAgent(_ int64, _ string) error         { return nil }
func (m *mockTUIState) SpawnSubagent(_ int64, _, _ string) error { return nil }

func (m *mockTUIState) SetModel(chatID int64, model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modelSet = append(m.modelSet, struct {
		chatID int64
		model  string
	}{chatID: chatID, model: model})
	for i := range m.sessions {
		if m.sessions[i].ChatID == chatID {
			m.sessions[i].Model = model
		}
	}
	return nil
}

func startServerWithHandle(t *testing.T, state control.StateProvider) (*control.Server, string, context.CancelFunc) {
	t.Helper()
	port := freePort(t)
	cfg := control.Config{
		Enabled:                   true,
		Port:                      port,
		AllowLoopbackWithoutToken: true,
	}
	srv := control.New(cfg, state)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("server error: %v", err)
		}
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	return srv, addr, cancel
}

func sendTUIRequest(t *testing.T, conn net.Conn, msg control.ClientMsg) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := wsutil.WriteClientText(conn, data); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

func readTUIMessage(t *testing.T, conn net.Conn) control.ServerMsg {
	t.Helper()
	conn.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	data, op, err := wsutil.ReadServerData(conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if op != ws.OpText {
		t.Fatalf("expected text frame, got %v", op)
	}
	var msg control.ServerMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return msg
}

func TestControlServerHandlesTUISessionCommands(t *testing.T) {
	state := &mockTUIState{
		sessions: []control.SessionInfo{
			{ChatID: 42, Username: "alice", Model: "model-a", State: "idle"},
		},
	}
	_, addr, cancel := startServerWithHandle(t, state)
	defer cancel()

	conn := wsConnect(t, addr)

	sendTUIRequest(t, conn, control.ClientMsg{Type: control.CmdListSessions})

	connected := readTUIMessage(t, conn)
	if connected.Type != control.MsgTypeConnected {
		t.Fatalf("expected %q, got %q", control.MsgTypeConnected, connected.Type)
	}
	if connected.SessionID != "42" {
		t.Fatalf("expected active session_id 42, got %q", connected.SessionID)
	}

	sessions := readTUIMessage(t, conn)
	if sessions.Type != control.MsgTypeSessions {
		t.Fatalf("expected %q, got %q", control.MsgTypeSessions, sessions.Type)
	}
	if len(sessions.Sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions.Sessions))
	}
	if sessions.Sessions[0].Model != "model-a" {
		t.Fatalf("expected model model-a, got %q", sessions.Sessions[0].Model)
	}

	sendTUIRequest(t, conn, control.ClientMsg{
		Type:      control.CmdSetModel,
		SessionID: "42",
		Model:     "model-b",
	})
	updated := readTUIMessage(t, conn)
	if updated.Type != control.MsgTypeSessions {
		t.Fatalf("expected %q after set_model, got %q", control.MsgTypeSessions, updated.Type)
	}
	if len(updated.Sessions) != 1 || updated.Sessions[0].Model != "model-b" {
		t.Fatalf("expected updated model model-b, got %#v", updated.Sessions)
	}
}

func TestControlServerHandlesTUISend(t *testing.T) {
	state := &mockTUIState{
		sessions: []control.SessionInfo{
			{ChatID: 777, Model: "model-a", State: "idle"},
		},
	}
	_, addr, cancel := startServerWithHandle(t, state)
	defer cancel()

	conn := wsConnect(t, addr)

	sendTUIRequest(t, conn, control.ClientMsg{Type: control.CmdListSessions})
	_ = readTUIMessage(t, conn) // connected
	_ = readTUIMessage(t, conn) // sessions

	sendTUIRequest(t, conn, control.ClientMsg{
		Type:      control.CmdSend,
		SessionID: "777",
		Text:      "hello from tui",
	})

	event := readTUIMessage(t, conn)
	if event.Type != control.MsgTypeEvent || event.Kind != control.KindMessage {
		t.Fatalf("expected user message event, got type=%q kind=%q", event.Type, event.Kind)
	}
	if event.SessionID != "777" {
		t.Fatalf("expected session_id 777, got %q", event.SessionID)
	}
	if event.Role != "user" || event.Content != "hello from tui" {
		t.Fatalf("unexpected message payload: role=%q content=%q", event.Role, event.Content)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sent) != 1 {
		t.Fatalf("expected exactly 1 SendChat call, got %d", len(state.sent))
	}
	if state.sent[0].chatID != 777 || state.sent[0].text != "hello from tui" {
		t.Fatalf("unexpected SendChat call: %+v", state.sent[0])
	}
}

func TestHubEmitMirrorsLegacyRunEventsToTUI(t *testing.T) {
	state := &mockTUIState{
		sessions: []control.SessionInfo{
			{ChatID: 99, Model: "model-a", State: "idle"},
		},
	}
	srv, addr, cancel := startServerWithHandle(t, state)
	defer cancel()

	conn := wsConnect(t, addr)

	// Initialize TUI state for this connection.
	sendTUIRequest(t, conn, control.ClientMsg{Type: control.CmdListSessions})
	_ = readTUIMessage(t, conn)
	_ = readTUIMessage(t, conn)

	srv.Hub().Emit(control.EvtRunStarted, control.RunEventPayload{ChatID: 99})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		msg := readTUIMessage(t, conn)
		if msg.Type == control.MsgTypeEvent && msg.Kind == control.KindRunStart {
			if msg.SessionID != "99" {
				t.Fatalf("expected session_id 99, got %q", msg.SessionID)
			}
			return
		}
	}
	t.Fatal("did not receive mirrored TUI run_start event")
}
