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

	"ok-gobot/internal/agent"
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
	tuiRunReqs  []control.TUIRunRequest
	tuiAbortKey []string
	tuiSubmitFn func(control.TUIRunRequest) <-chan agent.RunEvent
}

func (m *mockTUIState) GetStatus() map[string]interface{} {
	return map[string]interface{}{
		"ok": true,
		"ai": map[string]interface{}{
			"model": "model-a",
		},
	}
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
	return nil
}

func (m *mockTUIState) SubmitTUIRun(_ context.Context, req control.TUIRunRequest) <-chan agent.RunEvent {
	m.mu.Lock()
	m.tuiRunReqs = append(m.tuiRunReqs, req)
	submitFn := m.tuiSubmitFn
	m.mu.Unlock()

	if submitFn != nil {
		return submitFn(req)
	}

	ch := make(chan agent.RunEvent, 1)
	go func() {
		if req.OnDelta != nil {
			req.OnDelta("token")
		}
		ch <- agent.RunEvent{
			Type: agent.RunEventDone,
			Result: &agent.AgentResponse{
				Message:          "assistant reply",
				PromptTokens:     11,
				CompletionTokens: 7,
				TotalTokens:      18,
			},
		}
		close(ch)
	}()
	return ch
}

func (m *mockTUIState) AbortTUIRun(sessionKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tuiAbortKey = append(m.tuiAbortKey, sessionKey)
}

func (m *mockTUIState) LogTUIExchange(_, _ string) {}

func (m *mockTUIState) GetStatusText(_ string) string { return "ok" }

func (m *mockTUIState) IsEmergencyStopEnabled() (bool, error) { return false, nil }

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
	state := &mockTUIState{}
	_, addr, cancel := startServerWithHandle(t, state)
	defer cancel()

	conn := wsConnect(t, addr)

	sendTUIRequest(t, conn, control.ClientMsg{Type: control.CmdListSessions})

	connected := readTUIMessage(t, conn)
	if connected.Type != control.MsgTypeConnected {
		t.Fatalf("expected %q, got %q", control.MsgTypeConnected, connected.Type)
	}
	if connected.SessionID != "main" {
		t.Fatalf("expected active session_id main, got %q", connected.SessionID)
	}

	sessions := readTUIMessage(t, conn)
	if sessions.Type != control.MsgTypeSessions {
		t.Fatalf("expected %q, got %q", control.MsgTypeSessions, sessions.Type)
	}
	if len(sessions.Sessions) != 1 {
		t.Fatalf("expected 1 default TUI session, got %d", len(sessions.Sessions))
	}
	if sessions.Sessions[0].Model != "model-a" {
		t.Fatalf("expected default model model-a, got %q", sessions.Sessions[0].Model)
	}

	sendTUIRequest(t, conn, control.ClientMsg{
		Type:  control.CmdNewSession,
		Name:  "Scratch",
		Model: "model-x",
	})
	created := readTUIMessage(t, conn)
	if created.Type != control.MsgTypeConnected {
		t.Fatalf("expected %q after new_session, got %q", control.MsgTypeConnected, created.Type)
	}
	if created.SessionID == "main" {
		t.Fatal("expected a distinct new TUI session ID")
	}
	if len(created.Sessions) != 2 {
		t.Fatalf("expected 2 sessions after new_session, got %d", len(created.Sessions))
	}

	sendTUIRequest(t, conn, control.ClientMsg{
		Type:      control.CmdSetModel,
		SessionID: created.SessionID,
		Model:     "model-b",
	})
	updated := readTUIMessage(t, conn)
	if updated.Type != control.MsgTypeSessions {
		t.Fatalf("expected %q after set_model, got %q", control.MsgTypeSessions, updated.Type)
	}
	var gotModel string
	for _, s := range updated.Sessions {
		if s.ID == created.SessionID {
			gotModel = s.Model
			break
		}
	}
	if gotModel != "model-b" {
		t.Fatalf("expected updated model model-b, got %q", gotModel)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.modelSet) != 0 {
		t.Fatalf("expected no Telegram SetModel calls for TUI sessions, got %d", len(state.modelSet))
	}
}

func TestControlServerHandlesTUISend(t *testing.T) {
	state := &mockTUIState{}
	_, addr, cancel := startServerWithHandle(t, state)
	defer cancel()

	conn := wsConnect(t, addr)

	sendTUIRequest(t, conn, control.ClientMsg{Type: control.CmdListSessions})
	_ = readTUIMessage(t, conn) // connected
	_ = readTUIMessage(t, conn) // sessions

	sendTUIRequest(t, conn, control.ClientMsg{
		Type:      control.CmdSend,
		SessionID: "main",
		Text:      "hello from tui",
	})

	var (
		gotUser       bool
		gotStart      bool
		gotToken      bool
		gotAssist     bool
		gotAssistMeta bool
		gotRunEnd     bool
		deadline      = time.Now().Add(2 * time.Second)
	)

	for time.Now().Before(deadline) {
		msg := readTUIMessage(t, conn)
		if msg.Type != control.MsgTypeEvent {
			continue
		}
		switch msg.Kind {
		case control.KindMessage:
			if msg.Role == "user" && msg.Content == "hello from tui" && msg.SessionID == "main" && msg.Timestamp != "" {
				gotUser = true
			}
			if msg.Role == "assistant" && msg.Content == "assistant reply" && msg.SessionID == "main" {
				gotAssist = true
				if msg.Model == "model-a" && msg.PromptTokens == 11 && msg.CompletionTokens == 7 && msg.TotalTokens == 18 && msg.Timestamp != "" {
					if _, err := time.Parse(time.RFC3339, msg.Timestamp); err == nil {
						gotAssistMeta = true
					}
				}
			}
		case control.KindRunStart:
			if msg.SessionID == "main" && msg.Model == "model-a" && msg.Timestamp != "" {
				gotStart = true
			}
		case control.KindToken:
			if msg.Content == "token" && msg.SessionID == "main" {
				gotToken = true
			}
		case control.KindRunEnd:
			if msg.SessionID == "main" && msg.Model == "model-a" && msg.Timestamp != "" {
				gotRunEnd = true
			}
		}
		if gotUser && gotStart && gotToken && gotAssist && gotAssistMeta && gotRunEnd {
			break
		}
	}

	if !gotUser || !gotStart || !gotToken || !gotAssist || !gotAssistMeta || !gotRunEnd {
		t.Fatalf("missing expected TUI run events user=%v start=%v token=%v assistant=%v assistant_meta=%v run_end=%v", gotUser, gotStart, gotToken, gotAssist, gotAssistMeta, gotRunEnd)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.sent) != 0 {
		t.Fatalf("expected no SendChat calls, got %d", len(state.sent))
	}
	if len(state.tuiRunReqs) != 1 {
		t.Fatalf("expected one SubmitTUIRun call, got %d", len(state.tuiRunReqs))
	}
	gotReq := state.tuiRunReqs[0]
	if gotReq.SessionKey != "agent:default:tui:main" {
		t.Fatalf("unexpected session key: %q", gotReq.SessionKey)
	}
	if gotReq.Content != "hello from tui" {
		t.Fatalf("unexpected content: %q", gotReq.Content)
	}
}

func TestControlServerTUISendSuppressesBootstrapToolEvents(t *testing.T) {
	state := &mockTUIState{}
	state.tuiSubmitFn = func(req control.TUIRunRequest) <-chan agent.RunEvent {
		ch := make(chan agent.RunEvent, 1)
		go func() {
			if req.OnToolEvent != nil {
				req.OnToolEvent(agent.ToolEvent{
					ToolName: "file",
					Type:     agent.ToolEventStarted,
					Input:    `{"command":"read","path":"SOUL.md"}`,
				})
				req.OnToolEvent(agent.ToolEvent{
					ToolName: "file",
					Type:     agent.ToolEventFinished,
					Output:   "soul content",
				})
				req.OnToolEvent(agent.ToolEvent{
					ToolName: "memory_get",
					Type:     agent.ToolEventStarted,
					Input:    `{"source":"memory/2026-03-05.md"}`,
				})
				req.OnToolEvent(agent.ToolEvent{
					ToolName: "memory_get",
					Type:     agent.ToolEventFinished,
					Output:   "daily memory",
				})
				req.OnToolEvent(agent.ToolEvent{
					ToolName: "search",
					Type:     agent.ToolEventStarted,
					Input:    `{"query":"weather"}`,
				})
				req.OnToolEvent(agent.ToolEvent{
					ToolName: "search",
					Type:     agent.ToolEventFinished,
					Output:   "ok",
				})
			}
			if req.OnDelta != nil {
				req.OnDelta("token")
			}
			ch <- agent.RunEvent{
				Type: agent.RunEventDone,
				Result: &agent.AgentResponse{
					Message: "assistant reply",
				},
			}
			close(ch)
		}()
		return ch
	}

	_, addr, cancel := startServerWithHandle(t, state)
	defer cancel()

	conn := wsConnect(t, addr)

	sendTUIRequest(t, conn, control.ClientMsg{Type: control.CmdListSessions})
	_ = readTUIMessage(t, conn) // connected
	_ = readTUIMessage(t, conn) // sessions

	sendTUIRequest(t, conn, control.ClientMsg{
		Type:      control.CmdSend,
		SessionID: "main",
		Text:      "test bootstrap filtering",
	})

	var (
		sawBootstrapTool bool
		sawSearchStart   bool
		sawSearchEnd     bool
		sawRunEnd        bool
		deadline         = time.Now().Add(2 * time.Second)
	)

	for time.Now().Before(deadline) {
		msg := readTUIMessage(t, conn)
		if msg.Type != control.MsgTypeEvent {
			continue
		}
		switch msg.Kind {
		case control.KindToolStart:
			if msg.ToolName == "file" || msg.ToolName == "memory_get" {
				sawBootstrapTool = true
			}
			if msg.ToolName == "search" {
				sawSearchStart = true
			}
		case control.KindToolEnd:
			if msg.ToolName == "file" || msg.ToolName == "memory_get" {
				sawBootstrapTool = true
			}
			if msg.ToolName == "search" {
				sawSearchEnd = true
			}
		case control.KindRunEnd:
			sawRunEnd = true
		}
		if sawRunEnd {
			break
		}
	}

	if !sawRunEnd {
		t.Fatal("expected run_end event")
	}
	if sawBootstrapTool {
		t.Fatal("received bootstrap tool event that should be suppressed")
	}
	if !sawSearchStart || !sawSearchEnd {
		t.Fatalf("expected non-bootstrap tool start/end events, got start=%v end=%v", sawSearchStart, sawSearchEnd)
	}
}

func TestControlServerAbortRoutesToTUIRuntimeProvider(t *testing.T) {
	state := &mockTUIState{}
	_, addr, cancel := startServerWithHandle(t, state)
	defer cancel()

	conn := wsConnect(t, addr)

	sendTUIRequest(t, conn, control.ClientMsg{Type: control.CmdListSessions})
	_ = readTUIMessage(t, conn) // connected
	_ = readTUIMessage(t, conn) // sessions

	sendTUIRequest(t, conn, control.ClientMsg{
		Type:      control.CmdAbort,
		SessionID: "main",
	})

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		state.mu.Lock()
		if len(state.tuiAbortKey) > 0 {
			key := state.tuiAbortKey[0]
			state.mu.Unlock()
			if key != "agent:default:tui:main" {
				t.Fatalf("unexpected abort key: %q", key)
			}
			return
		}
		state.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected AbortTUIRun to be called")
}

func TestHubEmitMirrorsLegacyRunEventsToTUI(t *testing.T) {
	state := &mockTUIState{}
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

func TestControlServerRejectsLegacyMainProtocolFrames(t *testing.T) {
	state := &mockTUIState{}
	_, addr, cancel := startServerWithHandle(t, state)
	defer cancel()

	conn := wsConnect(t, addr)

	if err := wsutil.WriteClientText(conn, []byte(`{"id":"req-1","type":"status.get","payload":{}}`)); err != nil {
		t.Fatalf("write request: %v", err)
	}

	msg := readTUIMessage(t, conn)
	if msg.Type != control.MsgTypeError {
		t.Fatalf("expected %q, got %q", control.MsgTypeError, msg.Type)
	}
	if msg.Message != "unknown command type: status.get" {
		t.Fatalf("unexpected error message: %q", msg.Message)
	}
}
