package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	"ok-gobot/internal/ai"
)

func TestServerSessionProtocol(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	srv := NewTUIServer(TUIConfig{
		Addr:  addr,
		AICfg: testAICfg(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	waitForReady(t, addr)

	conn := dialTestWS(t, fmt.Sprintf("ws://%s/ws", addr))
	defer conn.conn.Close()

	connected := readServerMsg(t, conn)
	if connected.Type != MsgTypeConnected {
		t.Fatalf("expected initial message type %q, got %q", MsgTypeConnected, connected.Type)
	}
	if connected.SessionID == "" {
		t.Fatal("expected initial connected message to include a session ID")
	}
	if len(connected.Sessions) != 1 {
		t.Fatalf("expected exactly one default session, got %d", len(connected.Sessions))
	}

	writeClientMsg(t, conn, ClientMsg{Type: CmdListSessions})
	sessions := readServerMsg(t, conn)
	if sessions.Type != MsgTypeSessions {
		t.Fatalf("expected %q response, got %q", MsgTypeSessions, sessions.Type)
	}
	if len(sessions.Sessions) != 1 {
		t.Fatalf("expected one session in list response, got %d", len(sessions.Sessions))
	}

	writeClientMsg(t, conn, ClientMsg{
		Type:  CmdNewSession,
		Name:  "Scratch",
		Model: "openai/gpt-4o-mini",
	})
	created := readServerMsg(t, conn)
	if created.Type != MsgTypeConnected {
		t.Fatalf("expected %q after new session, got %q", MsgTypeConnected, created.Type)
	}
	if created.SessionID == connected.SessionID {
		t.Fatal("expected new session to become active")
	}
	if len(created.Sessions) != 2 {
		t.Fatalf("expected two sessions after creating one, got %d", len(created.Sessions))
	}

	writeClientMsg(t, conn, ClientMsg{
		Type:      CmdSetModel,
		SessionID: created.SessionID,
		Model:     "anthropic/claude-sonnet-4-5-20250929",
	})
	updated := readServerMsg(t, conn)
	if updated.Type != MsgTypeSessions {
		t.Fatalf("expected %q after set_model, got %q", MsgTypeSessions, updated.Type)
	}
	if got := sessionModel(updated.Sessions, created.SessionID); got != "anthropic/claude-sonnet-4-5-20250929" {
		t.Fatalf("expected updated model for %s, got %q", created.SessionID, got)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server returned error on shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}
}

func TestServerRejectsUnauthorizedWSUpgrade(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)
	srv := NewTUIServer(TUIConfig{
		Addr:                      addr,
		AICfg:                     testAICfg(),
		Token:                     "secret",
		AllowLoopbackWithoutToken: false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start(ctx)
	}()

	waitForReady(t, addr)

	resp, err := http.Get(fmt.Sprintf("http://%s/ws", addr))
	if err != nil {
		t.Fatalf("http get /ws: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected %d from unauthorized request, got %d", http.StatusUnauthorized, resp.StatusCode)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server returned error on shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server shutdown")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func testAICfg() ai.ProviderConfig {
	return ai.ProviderConfig{
		Name:  "openrouter",
		Model: "moonshotai/kimi-k2.5",
	}
}

func waitForReady(t *testing.T, addr string) {
	t.Helper()
	if err := WaitTUIReady(addr, 3*time.Second); err != nil {
		t.Fatalf("wait ready: %v", err)
	}
}

type testWSConn struct {
	conn net.Conn
	rw   io.ReadWriter
}

func dialTestWS(t *testing.T, url string) testWSConn {
	t.Helper()

	conn, br, _, err := ws.Dial(context.Background(), url)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	var reader io.Reader = conn
	if br != nil {
		reader = br
	}
	return testWSConn{
		conn: conn,
		rw: struct {
			io.Reader
			io.Writer
		}{reader, conn},
	}
}

func writeClientMsg(t *testing.T, conn testWSConn, msg ClientMsg) {
	t.Helper()

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal client msg: %v", err)
	}
	if err := wsutil.WriteClientText(conn.conn, data); err != nil {
		t.Fatalf("write client msg: %v", err)
	}
}

func readServerMsg(t *testing.T, conn testWSConn) ServerMsg {
	t.Helper()

	data, _, err := wsutil.ReadServerData(conn.rw)
	if err != nil {
		t.Fatalf("read server msg: %v", err)
	}

	var msg ServerMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal server msg: %v", err)
	}
	return msg
}

func sessionModel(sessions []TUISessionInfo, id string) string {
	for _, session := range sessions {
		if session.ID == id {
			return session.Model
		}
	}
	return ""
}
