// Package control provides WebSocket control servers for both runtime control
// flows and the standalone TUI surface.
// It manages AI sessions and broadcasts events to connected clients.
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	"ok-gobot/internal/ai"
	runtimepkg "ok-gobot/internal/runtime"
)

// TUIConfig holds standalone TUI control-server configuration.
type TUIConfig struct {
	Addr                      string // e.g. "127.0.0.1:9099"
	AICfg                     ai.ProviderConfig
	Token                     string
	AllowLoopbackWithoutToken bool
}

// TUIServer is the standalone WebSocket server used by the terminal UI.
type TUIServer struct {
	cfg        TUIConfig
	hub        *tuiHub
	manager    *Manager
	runtimeHub *runtimepkg.Hub
	http       *http.Server
}

// NewTUIServer creates a new standalone TUI control server.
func NewTUIServer(cfg TUIConfig) *TUIServer {
	hub := newTUIHub()
	return &TUIServer{
		cfg:     cfg,
		hub:     hub,
		manager: NewManager(hub, cfg.AICfg),
	}
}

// Manager returns the standalone TUI session manager.
func (s *TUIServer) Manager() *Manager {
	return s.manager
}

// Start begins listening on the configured address.
func (s *TUIServer) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("control server: listen %s: %w", s.cfg.Addr, err)
	}
	return s.ServeOn(ctx, ln)
}

// ServeOn starts the server using the provided listener. This allows callers
// (e.g. tests) to pre-allocate a listener and avoid TOCTOU port races.
func (s *TUIServer) ServeOn(ctx context.Context, ln net.Listener) error {
	// Initialise the runtime hub for sub-agent spawning and subscribe for events.
	runtimeCtx, runtimeCancel := context.WithCancel(ctx)
	defer runtimeCancel()
	s.runtimeHub = runtimepkg.NewHub(runtimeCtx, 64)

	evCh := make(chan runtimepkg.RuntimeEvent, 128)
	s.runtimeHub.Subscribe(evCh)
	go s.bridgeRuntimeEvents(runtimeCtx, evCh)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/health", s.handleHealth)

	s.http = &http.Server{
		Handler: mux,
	}

	// Ensure a default session exists
	if _, err := s.manager.NewSession("Chat", ""); err != nil {
		log.Printf("[controlserver] warning: could not create default session: %v", err)
	}

	log.Printf("[controlserver] listening on %s", ln.Addr())

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("control server: %w", err)
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return s.http.Shutdown(shutCtx)
	}
}

// handleHealth is a simple health check endpoint.
func (s *TUIServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","clients":%d}`, s.hub.Count())
}

// handleWS upgrades the connection to WebSocket and handles client messages.
func (s *TUIServer) handleWS(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Token != "" {
		loopback := isLoopback(r.RemoteAddr)
		if !loopback || !s.cfg.AllowLoopbackWithoutToken {
			if r.URL.Query().Get("token") != s.cfg.Token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
	}

	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		log.Printf("[controlserver] ws upgrade error: %v", err)
		return
	}

	client := &tuiClient{conn: conn}
	s.hub.add(client)
	defer func() {
		s.hub.remove(client)
		conn.Close()
	}()

	log.Printf("[controlserver] client connected from %s", conn.RemoteAddr())

	// Send the current session list on connect
	sessions := s.manager.List()
	activeID := ""
	if len(sessions) > 0 {
		activeID = sessions[0].ID
	}
	_ = client.send(ServerMsg{
		Type:      MsgTypeConnected,
		SessionID: activeID,
		Sessions:  sessions,
	})

	// Read loop
	for {
		data, op, err := wsutil.ReadClientData(conn)
		if err != nil {
			break
		}
		if op != ws.OpText {
			continue
		}

		var cmd ClientMsg
		if err := json.Unmarshal(data, &cmd); err != nil {
			log.Printf("[controlserver] bad client message: %v", err)
			continue
		}

		s.handleClientMsg(r.Context(), client, cmd)
	}
}

// handleClientMsg dispatches a client command.
func (s *TUIServer) handleClientMsg(ctx context.Context, client *tuiClient, cmd ClientMsg) {
	switch cmd.Type {
	case CmdSend:
		sess := s.getOrFirst(cmd.SessionID)
		if sess == nil {
			_ = client.send(ServerMsg{Type: MsgTypeError, Message: "no active session"})
			return
		}
		sess.Send(ctx, cmd.Text)

	case CmdAbort:
		sess := s.getOrFirst(cmd.SessionID)
		if sess != nil {
			sess.Abort()
		}

	case CmdApprove:
		sess := s.getOrFirst(cmd.SessionID)
		if sess != nil {
			sess.Approve(cmd.ApprovalID, cmd.Approved)
		}

	case CmdSetModel:
		if err := s.manager.SetModel(cmd.SessionID, cmd.Model); err != nil {
			_ = client.send(ServerMsg{Type: MsgTypeError, Message: err.Error()})
			return
		}
		_ = client.send(ServerMsg{
			Type:     MsgTypeSessions,
			Sessions: s.manager.List(),
		})

	case CmdListSessions:
		_ = client.send(ServerMsg{
			Type:     MsgTypeSessions,
			Sessions: s.manager.List(),
		})

	case CmdNewSession:
		sess, err := s.manager.NewSession(cmd.Name, cmd.Model)
		if err != nil {
			_ = client.send(ServerMsg{Type: MsgTypeError, Message: err.Error()})
			return
		}
		_ = client.send(ServerMsg{
			Type:      MsgTypeConnected,
			SessionID: sess.ID,
			Sessions:  s.manager.List(),
		})

	case CmdSwitch:
		sess := s.manager.Get(cmd.SessionID)
		if sess == nil {
			_ = client.send(ServerMsg{Type: MsgTypeError, Message: "session not found"})
			return
		}
		_ = client.send(ServerMsg{
			Type:      MsgTypeConnected,
			SessionID: sess.ID,
			Sessions:  s.manager.List(),
		})

	case CmdSpawnSubagent:
		s.handleSpawnSubagent(client, cmd)
	}
}

// getOrFirst returns the named session or the first available one.
func (s *TUIServer) getOrFirst(id string) *Session {
	if id != "" {
		return s.manager.Get(id)
	}
	list := s.manager.List()
	if len(list) == 0 {
		return nil
	}
	return s.manager.Get(list[0].ID)
}

// handleSpawnSubagent spawns a sub-agent run for the given TUI session.
//
// A synthetic parent key "agent:tui:<sessionID>" is constructed so that the
// runtime.Hub can route EventChildDone / EventChildFailed back to the session.
// The child session key is returned to the client immediately; completion is
// delivered asynchronously via a KindChildDone or KindChildFailed event.
func (s *TUIServer) handleSpawnSubagent(client *tuiClient, cmd ClientMsg) {
	if s.runtimeHub == nil {
		_ = client.send(ServerMsg{Type: MsgTypeError, Message: "runtime hub not ready"})
		return
	}

	// Construct a valid parent key from the TUI session ID.
	parentKey := "agent:tui:" + cmd.SessionID

	req := runtimepkg.SubagentSpawnRequest{
		ParentSessionKey: parentKey,
		Task:             cmd.Task,
		Model:            cmd.Model,
		Thinking:         cmd.Thinking,
		ToolAllowlist:    cmd.ToolAllowlist,
		WorkspaceRoot:    cmd.WorkspaceRoot,
		DeliverBack:      true,
	}

	handle, err := s.runtimeHub.SpawnSubagent(req, func(ctx context.Context, ack runtimepkg.AckHandle) {
		// Task execution is out of scope for Phase E; the spawn API manages
		// lifecycle and routing.  Real task execution is wired at the agent layer.
		log.Printf("[controlserver] subagent %s started: task=%q", req.Task, req.Task)
		ack.Close(nil)
	})
	if err != nil {
		_ = client.send(ServerMsg{Type: MsgTypeError, Message: fmt.Sprintf("spawn subagent: %v", err)})
		return
	}

	_ = client.send(ServerMsg{
		Type:            MsgTypeEvent,
		Kind:            KindRunStart,
		SessionID:       cmd.SessionID,
		ChildSessionKey: handle.SessionKey,
	})

	log.Printf("[controlserver] spawned subagent %s for session %s", handle.SessionKey, cmd.SessionID)
}

// bridgeRuntimeEvents forwards EventChildDone and EventChildFailed from the
// runtime hub to all connected WebSocket clients, translating the synthetic
// parent key back to the original TUI session ID.
func (s *TUIServer) bridgeRuntimeEvents(ctx context.Context, evCh <-chan runtimepkg.RuntimeEvent) {
	const parentPrefix = "agent:tui:"
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-evCh:
			if !ok {
				return
			}
			if ev.Type != runtimepkg.EventChildDone && ev.Type != runtimepkg.EventChildFailed {
				continue
			}

			// ev.SessionKey = "agent:tui:<sessionID>"; strip prefix to get session ID.
			if !strings.HasPrefix(ev.SessionKey, parentPrefix) {
				continue
			}
			tuiSessionID := strings.TrimPrefix(ev.SessionKey, parentPrefix)

			payload, ok := ev.Payload.(runtimepkg.ChildCompletionPayload)
			if !ok {
				continue
			}

			kind := KindChildDone
			errMsg := ""
			if ev.Type == runtimepkg.EventChildFailed {
				kind = KindChildFailed
				if payload.Err != nil {
					errMsg = payload.Err.Error()
				}
			}

			s.hub.Broadcast(ServerMsg{
				Type:            MsgTypeEvent,
				Kind:            kind,
				SessionID:       tuiSessionID,
				ChildSessionKey: payload.ChildSessionKey,
				Message:         errMsg,
			})
		}
	}
}

// WaitTUIReady polls until the TUI server is accepting connections at addr.
func WaitTUIReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("control server at %s not ready after %s", addr, timeout)
}

// ListenAndServeOn is a helper that binds a free TCP address and calls ServeOn.
// It sends the chosen address on addrCh before blocking.  Useful for tests.
func (s *TUIServer) ListenAndServeOn(ctx context.Context, addrCh chan<- string) error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		close(addrCh)
		return err
	}
	addrCh <- ln.Addr().String()
	return s.ServeOn(ctx, ln)
}
