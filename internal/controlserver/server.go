// Package controlserver provides a WebSocket control server for the TUI client.
// It manages AI sessions and broadcasts events to connected clients.
package controlserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

	"ok-gobot/internal/ai"
)

// Config holds control server configuration.
type Config struct {
	Addr  string // e.g. "127.0.0.1:9099"
	AICfg ai.ProviderConfig
}

// Server is the control server.
type Server struct {
	cfg     Config
	hub     *Hub
	manager *Manager
	http    *http.Server
}

// New creates a new control server.
func New(cfg Config) *Server {
	hub := NewHub()
	return &Server{
		cfg:     cfg,
		hub:     hub,
		manager: NewManager(hub, cfg.AICfg),
	}
}

// Manager returns the session manager (for external inspection).
func (s *Server) Manager() *Manager {
	return s.manager
}

// Hub returns the event hub.
func (s *Server) Hub() *Hub {
	return s.hub
}

// Start begins listening on the configured address.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/health", s.handleHealth)

	s.http = &http.Server{
		Addr:    s.cfg.Addr,
		Handler: mux,
	}

	// Ensure a default session exists
	if _, err := s.manager.NewSession("Chat", ""); err != nil {
		log.Printf("[controlserver] warning: could not create default session: %v", err)
	}

	log.Printf("[controlserver] listening on %s", s.cfg.Addr)

	errCh := make(chan error, 1)
	go func() {
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","clients":%d}`, s.hub.Count())
}

// handleWS upgrades the connection to WebSocket and handles client messages.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		log.Printf("[controlserver] ws upgrade error: %v", err)
		return
	}

	client := &wsClient{conn: conn}
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
func (s *Server) handleClientMsg(ctx context.Context, client *wsClient, cmd ClientMsg) {
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
	}
}

// getOrFirst returns the named session or the first available one.
func (s *Server) getOrFirst(id string) *Session {
	if id != "" {
		return s.manager.Get(id)
	}
	list := s.manager.List()
	if len(list) == 0 {
		return nil
	}
	return s.manager.Get(list[0].ID)
}

// WaitReady polls until the server is accepting connections.
func WaitReady(addr string, timeout time.Duration) error {
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
