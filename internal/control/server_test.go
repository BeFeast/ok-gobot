package control_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	"ok-gobot/internal/control"
)

// mockState implements control.StateProvider for tests.
type mockState struct {
	status   map[string]interface{}
	sessions []control.SessionInfo
}

func (m *mockState) GetStatus() map[string]interface{}            { return m.status }
func (m *mockState) ListSessions() ([]control.SessionInfo, error) { return m.sessions, nil }
func (m *mockState) SendChat(_ int64, _ string) error             { return nil }
func (m *mockState) AbortRun(_ int64) error                       { return nil }
func (m *mockState) RespondToApproval(_ string, _ bool) error     { return nil }
func (m *mockState) SetModel(_ int64, _ string) error             { return nil }
func (m *mockState) SetAgent(_ int64, _ string) error             { return nil }
func (m *mockState) SpawnSubagent(_ int64, _, _ string) error     { return nil }

// freePort returns a free TCP port on the loopback interface.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// startServer starts a control server on a free port and returns the address and a
// cancel func that shuts the server down.
func startServer(t *testing.T, state control.StateProvider, token string) (addr string, cancel context.CancelFunc) {
	t.Helper()
	port := freePort(t)
	cfg := control.Config{
		Enabled:                   true,
		Port:                      port,
		Token:                     token,
		AllowLoopbackWithoutToken: true,
	}
	srv := control.New(cfg, state)
	ctx, cancelFn := context.WithCancel(context.Background())
	ready := make(chan struct{})
	go func() {
		close(ready)
		if err := srv.Start(ctx); err != nil && ctx.Err() == nil {
			t.Errorf("server error: %v", err)
		}
	}()
	<-ready
	// Wait until the server is accepting connections.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Sprintf("127.0.0.1:%d", port), cancelFn
}

// wsConnect dials the control server and upgrades to WebSocket.
func wsConnect(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, _, _, err := ws.Dial(context.Background(), "ws://"+addr+"/ws")
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// sendRequest serialises and sends a request message.
func sendRequest(t *testing.T, conn net.Conn, req control.Message) {
	t.Helper()
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if err := wsutil.WriteClientText(conn, data); err != nil {
		t.Fatalf("write request: %v", err)
	}
}

// readResponse reads and decodes the next WebSocket message from the server.
func readResponse(t *testing.T, conn net.Conn) control.Message {
	t.Helper()
	conn.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	data, op, err := wsutil.ReadServerData(conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if op != ws.OpText {
		t.Fatalf("expected text frame, got %v", op)
	}
	var msg control.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return msg
}

// TestDefaultConfig verifies the default configuration values.
func TestDefaultConfig(t *testing.T) {
	cfg := control.DefaultConfig()
	if cfg.Enabled {
		t.Error("expected DefaultConfig.Enabled = false (disabled by default for security)")
	}
	if cfg.Port != 8787 {
		t.Errorf("expected DefaultConfig.Port = 8787, got %d", cfg.Port)
	}
	if !cfg.AllowLoopbackWithoutToken {
		t.Error("expected DefaultConfig.AllowLoopbackWithoutToken = true")
	}
}

// TestServerStartsAndAcceptsConnections verifies that the server starts up and
// a WebSocket client can connect.
func TestServerStartsAndAcceptsConnections(t *testing.T) {
	state := &mockState{status: map[string]interface{}{"ok": true}}
	addr, cancel := startServer(t, state, "")
	defer cancel()

	conn := wsConnect(t, addr)
	_ = conn
}

// TestStatusGet verifies the status.get request returns the mock status.
func TestStatusGet(t *testing.T) {
	state := &mockState{status: map[string]interface{}{"running": true, "model": "test"}}
	addr, cancel := startServer(t, state, "")
	defer cancel()

	conn := wsConnect(t, addr)
	sendRequest(t, conn, control.Message{
		ID:   "req-1",
		Type: control.ReqStatusGet,
	})
	resp := readResponse(t, conn)

	if resp.ID != "req-1" {
		t.Errorf("expected ID req-1, got %q", resp.ID)
	}
	if resp.Type != control.ReqStatusGet {
		t.Errorf("expected type %q, got %q", control.ReqStatusGet, resp.Type)
	}
	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
	if resp.Payload == nil {
		t.Fatal("expected non-nil payload")
	}
}

// TestSessionsList verifies the sessions.list request.
func TestSessionsList(t *testing.T) {
	state := &mockState{
		sessions: []control.SessionInfo{
			{ChatID: 42, State: "idle"},
			{ChatID: 99, State: "running"},
		},
	}
	addr, cancel := startServer(t, state, "")
	defer cancel()

	conn := wsConnect(t, addr)
	sendRequest(t, conn, control.Message{
		ID:   "req-2",
		Type: control.ReqSessionsList,
	})
	resp := readResponse(t, conn)

	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
	var sessions []control.SessionInfo
	if err := json.Unmarshal(resp.Payload, &sessions); err != nil {
		t.Fatalf("unmarshal sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Errorf("expected 2 sessions, got %d", len(sessions))
	}
}

// TestSessionSelect verifies the session.select request.
func TestSessionSelect(t *testing.T) {
	state := &mockState{}
	addr, cancel := startServer(t, state, "")
	defer cancel()

	conn := wsConnect(t, addr)

	payload, _ := json.Marshal(control.SessionSelectPayload{ChatID: 123})
	sendRequest(t, conn, control.Message{
		ID:      "req-3",
		Type:    control.ReqSessionSelect,
		Payload: json.RawMessage(payload),
	})
	resp := readResponse(t, conn)

	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

// TestUnknownRequest verifies that unknown request types return an error.
func TestUnknownRequest(t *testing.T) {
	state := &mockState{}
	addr, cancel := startServer(t, state, "")
	defer cancel()

	conn := wsConnect(t, addr)
	sendRequest(t, conn, control.Message{
		ID:   "req-unknown",
		Type: "unknown.request.type",
	})
	resp := readResponse(t, conn)

	if resp.Error == "" {
		t.Error("expected error for unknown request type")
	}
}

// TestHubEmit verifies that events emitted via Hub.Emit are delivered to clients.
func TestHubEmit(t *testing.T) {
	state := &mockState{}
	port := freePort(t)
	cfg := control.Config{
		Enabled:                   true,
		Port:                      port,
		AllowLoopbackWithoutToken: true,
	}
	srv := control.New(cfg, state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { srv.Start(ctx) }() //nolint:errcheck

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	conn, _, _, err := ws.Dial(context.Background(), "ws://"+addr+"/ws")
	if err != nil {
		t.Fatalf("ws dial: %v", err)
	}
	defer conn.Close()

	// Give the server a moment to register the client.
	time.Sleep(50 * time.Millisecond)

	// Emit a run.started event.
	srv.Hub().Emit(control.EvtRunStarted, control.RunEventPayload{ChatID: 777})

	conn.SetDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
	data, op, err := wsutil.ReadServerData(conn)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if op != ws.OpText {
		t.Fatalf("expected text frame, got %v", op)
	}

	var msg control.Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if msg.Type != control.EvtRunStarted {
		t.Errorf("expected type %q, got %q", control.EvtRunStarted, msg.Type)
	}
}

// TestBearerTokenAuth verifies that clients without a required token are rejected.
func TestBearerTokenAuth(t *testing.T) {
	state := &mockState{}
	port := freePort(t)
	cfg := control.Config{
		Enabled:                   true,
		Port:                      port,
		Token:                     "secret",
		AllowLoopbackWithoutToken: false, // force token check even for loopback
	}
	srv := control.New(cfg, state)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { srv.Start(ctx) }() //nolint:errcheck

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Connection without token should fail with 401.
	_, _, _, err := ws.Dial(context.Background(), "ws://"+addr+"/ws")
	if err == nil {
		t.Fatal("expected connection without token to be rejected")
	}

	// Connection with correct token in query string should succeed.
	conn, _, _, err := ws.Dial(context.Background(), "ws://"+addr+"/ws?token=secret")
	if err != nil {
		t.Fatalf("expected connection with correct token to succeed: %v", err)
	}
	conn.Close()
}

// TestApprovalRespondRequest verifies the approval.respond request.
func TestApprovalRespondRequest(t *testing.T) {
	state := &mockState{}
	addr, cancel := startServer(t, state, "")
	defer cancel()

	conn := wsConnect(t, addr)
	payload, _ := json.Marshal(control.ApprovalRespondPayload{
		ApprovalID: "appr-123",
		Approved:   true,
	})
	sendRequest(t, conn, control.Message{
		ID:      "req-appr",
		Type:    control.ReqApprovalRespond,
		Payload: json.RawMessage(payload),
	})
	resp := readResponse(t, conn)

	if resp.Error != "" {
		t.Errorf("unexpected error: %s", resp.Error)
	}
}

// TestInvalidJSONRequest verifies that invalid JSON returns an error response.
func TestInvalidJSONRequest(t *testing.T) {
	state := &mockState{}
	addr, cancel := startServer(t, state, "")
	defer cancel()

	conn := wsConnect(t, addr)

	// Send raw invalid JSON.
	if err := wsutil.WriteClientText(conn, []byte(`{invalid json`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := readResponse(t, conn)
	if resp.Error == "" {
		t.Error("expected error for invalid JSON")
	}
}
