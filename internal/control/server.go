// Package control implements a loopback-only WebSocket control server used by
// the TUI and other local consumers.  It exposes session state, run events,
// approval requests, and mutation methods over a simple JSON protocol.
//
// The server binds exclusively to 127.0.0.1 so it is never reachable from the
// network.  An optional token can be configured for additional authentication.
package control

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"

	"github.com/gobwas/ws"

	runtimepkg "ok-gobot/internal/runtime"
)

// StateProvider is the interface the control server uses to interact with the
// rest of the application.  Implement it on the bot (or a thin adapter) and
// pass it to New.
type StateProvider interface {
	// GetStatus returns a generic status map (same shape as the HTTP /api/status).
	GetStatus() map[string]interface{}

	// ListSessions returns the currently known chat sessions.
	ListSessions() ([]SessionInfo, error)

	// SendChat sends a text message to the given chat.
	SendChat(chatID int64, text string) error

	// AbortRun cancels the active run for the given chat, if any.
	AbortRun(chatID int64) error

	// RespondToApproval approves or rejects a pending approval by ID.
	RespondToApproval(id string, approved bool) error

	// SetModel overrides the model used for the given chat.
	SetModel(chatID int64, model string) error

	// SetAgent switches the active agent for the given chat.
	SetAgent(chatID int64, agent string) error

	// SpawnSubagent is a legacy alias used by older control clients; the
	// application may map it to an explicit background job launch.
	SpawnSubagent(parentChatID int64, task, agent string) error
}

// Config holds configuration for the control server.
type Config struct {
	// Enabled activates the server.  Enabled by default.
	Enabled bool `mapstructure:"enabled"`

	// Port is the TCP port to listen on (default 8787).
	Port int `mapstructure:"port"`

	// Token, when non-empty, requires clients to supply it via the
	// Authorization: Bearer <token> header or ?token=<value> query parameter.
	// Ignored for loopback connections when AllowLoopbackWithoutToken is true
	// (which is the default).
	Token string `mapstructure:"token"`

	// AllowLoopbackWithoutToken skips token verification for connections from
	// 127.0.0.1 (default true).
	AllowLoopbackWithoutToken bool `mapstructure:"allow_loopback_without_token"`
}

// DefaultConfig returns a Config with sensible defaults.
// Control is disabled by default; enable explicitly in config.yaml.
func DefaultConfig() Config {
	return Config{
		Enabled:                   false,
		Port:                      8787,
		AllowLoopbackWithoutToken: true,
	}
}

// Server is the loopback WebSocket control server.
type Server struct {
	cfg        Config
	hub        *Hub
	state      StateProvider
	httpSrv    *http.Server
	runtimeHub *runtimepkg.Hub
	tuiMu      sync.Mutex
	tuiState   *tuiSessionStore
}

// New creates a new Server.  Call Start to begin accepting connections.
func New(cfg Config, state StateProvider) *Server {
	hub := NewHub()
	return &Server{
		cfg:   cfg,
		hub:   hub,
		state: state,
		tuiState: &tuiSessionStore{
			byID: make(map[string]*tuiSessionState),
		},
	}
}

// Hub returns the event hub so callers can emit events from elsewhere in the
// application (e.g. bot callbacks, streaming AI responses).
func (s *Server) Hub() *Hub {
	return s.hub
}

// Start begins listening on 127.0.0.1:<port> and blocks until ctx is
// cancelled.
func (s *Server) Start(ctx context.Context) error {
	go s.hub.Run()
	s.initTUIRuntime(ctx)

	addr := fmt.Sprintf("127.0.0.1:%d", s.cfg.Port)
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)

	s.httpSrv = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("control: listen %s: %w", addr, err)
	}
	log.Printf("[control] WS server listening on ws://%s/ws", addr)

	// Stop the server when ctx is cancelled.
	go func() {
		<-ctx.Done()
		_ = s.httpSrv.Shutdown(context.Background())
	}()

	if err := s.httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("control: serve: %w", err)
	}
	return nil
}

// extractBearerToken returns the token from an Authorization: Bearer <token>
// header, or an empty string if not present.
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && auth[:7] == "Bearer " {
		return auth[7:]
	}
	return ""
}

// handleWS upgrades an HTTP connection to WebSocket and hands it to the hub.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	// Origin check: reject cross-origin WebSocket connections to prevent CSWSH.
	if !s.validateOrigin(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}

	// Token check: required when the connection is not from loopback (or when
	// AllowLoopbackWithoutToken is false).
	// Accepts token via Authorization: Bearer header or ?token= query param.
	if s.cfg.Token != "" {
		loopback := isLoopback(r.RemoteAddr)
		if !loopback || !s.cfg.AllowLoopbackWithoutToken {
			supplied := extractBearerToken(r)
			if supplied == "" {
				supplied = r.URL.Query().Get("token")
			}
			if !secureTokenCompare(supplied, s.cfg.Token) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
	}

	conn, _, _, err := ws.UpgradeHTTP(r, w)
	if err != nil {
		log.Printf("[control] upgrade error: %v", err)
		return
	}

	s.hub.addClient(conn, s)
}

// validateOrigin checks the Origin header to prevent cross-site WebSocket hijacking.
// Allows: missing Origin (non-browser clients), loopback origins, and same-port origins.
func (s *Server) validateOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients (CLI, TUI) don't send Origin
	}
	// Allow loopback origins only.
	allowedPrefixes := []string{
		fmt.Sprintf("http://127.0.0.1:%d", s.cfg.Port),
		fmt.Sprintf("http://localhost:%d", s.cfg.Port),
		"http://127.0.0.1",
		"http://localhost",
	}
	for _, prefix := range allowedPrefixes {
		if origin == prefix {
			return true
		}
	}
	log.Printf("[control] rejected WebSocket connection from origin %q", origin)
	return false
}

// secureTokenCompare performs constant-time comparison to prevent timing attacks.
func secureTokenCompare(supplied, expected string) bool {
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) == 1
}

// handleRequest dispatches an incoming client request and returns the response
// message (or nil if no response should be sent).
func (s *Server) handleRequest(req Message) *Message {
	switch req.Type {
	case ReqStatusGet:
		return s.reqStatusGet(req)
	case ReqSessionsList:
		return s.reqSessionsList(req)
	case ReqSessionSelect:
		return s.reqSessionSelect(req)
	case ReqChatSend:
		return s.reqChatSend(req)
	case ReqRunAbort:
		return s.reqRunAbort(req)
	case ReqAgentSet:
		return s.reqAgentSet(req)
	case ReqModelSet:
		return s.reqModelSet(req)
	case ReqSubagentSpawn:
		return s.reqSubagentSpawn(req)
	case ReqApprovalRespond:
		return s.reqApprovalRespond(req)
	default:
		resp := ErrorResponse(req.ID, req.Type, "unknown request type: "+req.Type)
		return &resp
	}
}

// --- request handlers ---

func (s *Server) reqStatusGet(req Message) *Message {
	status := s.state.GetStatus()
	resp, err := OKResponse(req.ID, req.Type, status)
	if err != nil {
		r := ErrorResponse(req.ID, req.Type, err.Error())
		return &r
	}
	return &resp
}

func (s *Server) reqSessionsList(req Message) *Message {
	sessions, err := s.state.ListSessions()
	if err != nil {
		r := ErrorResponse(req.ID, req.Type, err.Error())
		return &r
	}
	resp, err := OKResponse(req.ID, req.Type, sessions)
	if err != nil {
		r := ErrorResponse(req.ID, req.Type, err.Error())
		return &r
	}
	return &resp
}

func (s *Server) reqSessionSelect(req Message) *Message {
	var p SessionSelectPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		r := ErrorResponse(req.ID, req.Type, "invalid payload: "+err.Error())
		return &r
	}
	// Emit an accepted event so TUI can track active session.
	s.hub.Emit(EvtSessionAccepted, SessionInfo{ChatID: p.ChatID, State: "idle"})
	resp, _ := OKResponse(req.ID, req.Type, map[string]int64{"chat_id": p.ChatID})
	return &resp
}

func (s *Server) reqChatSend(req Message) *Message {
	var p ChatSendPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		r := ErrorResponse(req.ID, req.Type, "invalid payload: "+err.Error())
		return &r
	}
	if err := s.state.SendChat(p.ChatID, p.Text); err != nil {
		r := ErrorResponse(req.ID, req.Type, err.Error())
		return &r
	}
	resp, _ := OKResponse(req.ID, req.Type, map[string]bool{"ok": true})
	return &resp
}

func (s *Server) reqRunAbort(req Message) *Message {
	var p RunAbortPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		r := ErrorResponse(req.ID, req.Type, "invalid payload: "+err.Error())
		return &r
	}
	if err := s.state.AbortRun(p.ChatID); err != nil {
		r := ErrorResponse(req.ID, req.Type, err.Error())
		return &r
	}
	resp, _ := OKResponse(req.ID, req.Type, map[string]bool{"ok": true})
	return &resp
}

func (s *Server) reqAgentSet(req Message) *Message {
	var p AgentSetPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		r := ErrorResponse(req.ID, req.Type, "invalid payload: "+err.Error())
		return &r
	}
	if err := s.state.SetAgent(p.ChatID, p.Agent); err != nil {
		r := ErrorResponse(req.ID, req.Type, err.Error())
		return &r
	}
	resp, _ := OKResponse(req.ID, req.Type, map[string]bool{"ok": true})
	return &resp
}

func (s *Server) reqModelSet(req Message) *Message {
	var p ModelSetPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		r := ErrorResponse(req.ID, req.Type, "invalid payload: "+err.Error())
		return &r
	}
	if err := s.state.SetModel(p.ChatID, p.Model); err != nil {
		r := ErrorResponse(req.ID, req.Type, err.Error())
		return &r
	}
	resp, _ := OKResponse(req.ID, req.Type, map[string]bool{"ok": true})
	return &resp
}

func (s *Server) reqSubagentSpawn(req Message) *Message {
	var p SubagentSpawnPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		r := ErrorResponse(req.ID, req.Type, "invalid payload: "+err.Error())
		return &r
	}
	if err := s.state.SpawnSubagent(p.ParentChatID, p.Task, p.Agent); err != nil {
		r := ErrorResponse(req.ID, req.Type, err.Error())
		return &r
	}
	resp, _ := OKResponse(req.ID, req.Type, map[string]bool{"ok": true})
	return &resp
}

func (s *Server) reqApprovalRespond(req Message) *Message {
	var p ApprovalRespondPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		r := ErrorResponse(req.ID, req.Type, "invalid payload: "+err.Error())
		return &r
	}
	if err := s.state.RespondToApproval(p.ApprovalID, p.Approved); err != nil {
		r := ErrorResponse(req.ID, req.Type, err.Error())
		return &r
	}
	s.hub.Emit(EvtApprovalResolved, ApprovalResolvedPayload{
		ApprovalID: p.ApprovalID,
		Approved:   p.Approved,
	})
	resp, _ := OKResponse(req.ID, req.Type, map[string]bool{"ok": true})
	return &resp
}

// isLoopback reports whether addr (host:port) refers to the loopback interface.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
