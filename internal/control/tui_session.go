package control

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"ok-gobot/internal/ai"
)

// UsageStats holds cumulative token usage for a session.
type UsageStats struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Rounds           int
}

// ContextInfo holds context window information.
type ContextInfo struct {
	Messages        int
	EstimatedTokens int
}

// Session holds one conversation with the AI.
type Session struct {
	ID       string
	Name     string
	Model    string
	Messages []ai.ChatMessage

	mu          sync.Mutex
	cancelFn    context.CancelFunc
	running     bool
	hub         *tuiHub
	aiClient    ai.Client
	aiCfg       ai.ProviderConfig
	approvals   map[string]*ApprovalRequest
	approvalsMu sync.Mutex
	usage       UsageStats
}

// Manager manages multiple sessions.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	hub      *tuiHub
	aiCfg    ai.ProviderConfig
}

// NewManager creates a new session manager.
func NewManager(hub *tuiHub, aiCfg ai.ProviderConfig) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		hub:      hub,
		aiCfg:    aiCfg,
	}
}

// NewSession creates and registers a new session.
func (m *Manager) NewSession(name, model string) (*Session, error) {
	cfg := m.aiCfg
	if model != "" {
		cfg.Model = model
	}

	aiClient, err := ai.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("create ai client: %w", err)
	}

	id := fmt.Sprintf("sess-%d", time.Now().UnixMilli())
	if name == "" {
		name = "Chat"
	}

	s := &Session{
		ID:        id,
		Name:      name,
		Model:     cfg.Model,
		hub:       m.hub,
		aiClient:  aiClient,
		aiCfg:     cfg,
		approvals: make(map[string]*ApprovalRequest),
	}

	m.mu.Lock()
	m.sessions[id] = s
	m.mu.Unlock()

	return s, nil
}

// Get returns a session by ID.
func (m *Manager) Get(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// List returns info about all TUI sessions.
func (m *Manager) List() []TUISessionInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]TUISessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		running := s.running
		s.mu.Unlock()
		result = append(result, TUISessionInfo{
			ID:      s.ID,
			Name:    s.Name,
			Model:   s.Model,
			Running: running,
		})
	}
	return result
}

// SetModel changes the AI model for a session.
func (m *Manager) SetModel(id, model string) error {
	s := m.Get(id)
	if s == nil {
		return fmt.Errorf("session not found: %s", id)
	}
	cfg := m.aiCfg
	cfg.Model = model
	aiClient, err := ai.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("create ai client: %w", err)
	}
	s.mu.Lock()
	s.Model = model
	s.aiClient = aiClient
	s.aiCfg = cfg
	s.mu.Unlock()
	return nil
}

// Send processes a user message asynchronously.
func (s *Session) Send(ctx context.Context, text string) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		s.hub.Broadcast(ServerMsg{
			Type:      MsgTypeEvent,
			Kind:      KindError,
			SessionID: s.ID,
			Message:   "A run is already in progress. Use /abort first.",
		})
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancelFn = cancel
	s.running = true
	s.mu.Unlock()

	// Record user message
	s.mu.Lock()
	s.Messages = append(s.Messages, ai.ChatMessage{
		Role:    ai.RoleUser,
		Content: text,
	})
	s.mu.Unlock()

	// Notify clients
	s.hub.Broadcast(ServerMsg{
		Type:      MsgTypeEvent,
		Kind:      KindMessage,
		SessionID: s.ID,
		Role:      "user",
		Content:   text,
	})

	go func() {
		defer func() {
			s.mu.Lock()
			s.running = false
			s.cancelFn = nil
			s.mu.Unlock()
			cancel()
		}()

		s.hub.Broadcast(ServerMsg{
			Type:      MsgTypeEvent,
			Kind:      KindRunStart,
			SessionID: s.ID,
		})

		s.processRound(runCtx)

		s.hub.Broadcast(ServerMsg{
			Type:      MsgTypeEvent,
			Kind:      KindRunEnd,
			SessionID: s.ID,
		})
	}()
}

// processRound runs one round of the AI conversation loop.
func (s *Session) processRound(ctx context.Context) {
	s.mu.Lock()
	msgs := make([]ai.ChatMessage, len(s.Messages))
	copy(msgs, s.Messages)
	aiClient := s.aiClient
	s.mu.Unlock()

	// Try streaming first
	if sc, ok := aiClient.(ai.StreamingClient); ok {
		s.processStream(ctx, sc, msgs)
		return
	}
	// Fallback: non-streaming
	s.processNonStream(ctx, aiClient, msgs)
}

// processStream uses the streaming API.
func (s *Session) processStream(ctx context.Context, sc ai.StreamingClient, msgs []ai.ChatMessage) {
	stream := sc.CompleteStreamWithTools(ctx, msgs, nil)
	var buf strings.Builder

	for chunk := range stream {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if chunk.Error != nil {
			if ctx.Err() == nil {
				s.hub.Broadcast(ServerMsg{
					Type:      MsgTypeEvent,
					Kind:      KindError,
					SessionID: s.ID,
					Message:   chunk.Error.Error(),
				})
			}
			return
		}

		content := chunk.Content
		// Skip tool-call marker (tool calling not wired up in TUI yet)
		if strings.HasPrefix(content, "\n__TOOL_CALLS__:") {
			continue
		}

		if content != "" {
			buf.WriteString(content)
			s.hub.Broadcast(ServerMsg{
				Type:      MsgTypeEvent,
				Kind:      KindToken,
				SessionID: s.ID,
				Content:   content,
			})
		}
	}

	finalText := buf.String()
	if finalText == "" {
		return
	}

	s.mu.Lock()
	s.Messages = append(s.Messages, ai.ChatMessage{
		Role:    ai.RoleAssistant,
		Content: finalText,
	})
	s.mu.Unlock()

	s.hub.Broadcast(ServerMsg{
		Type:      MsgTypeEvent,
		Kind:      KindMessage,
		SessionID: s.ID,
		Role:      "assistant",
		Content:   finalText,
	})
}

// processNonStream falls back to non-streaming and simulates token delivery.
func (s *Session) processNonStream(ctx context.Context, client ai.Client, msgs []ai.ChatMessage) {
	legacyMsgs := make([]ai.Message, len(msgs))
	for i, m := range msgs {
		legacyMsgs[i] = ai.Message{Role: m.Role, Content: m.Content}
	}

	text, err := client.Complete(ctx, legacyMsgs)
	if err != nil {
		if ctx.Err() == nil {
			s.hub.Broadcast(ServerMsg{
				Type:      MsgTypeEvent,
				Kind:      KindError,
				SessionID: s.ID,
				Message:   err.Error(),
			})
		}
		return
	}

	// Simulate streaming at ~80 chars/tick
	const chunkSize = 8
	for i := 0; i < len(text); i += chunkSize {
		select {
		case <-ctx.Done():
			return
		default:
		}
		end := i + chunkSize
		if end > len(text) {
			end = len(text)
		}
		s.hub.Broadcast(ServerMsg{
			Type:      MsgTypeEvent,
			Kind:      KindToken,
			SessionID: s.ID,
			Content:   text[i:end],
		})
		time.Sleep(10 * time.Millisecond)
	}

	s.mu.Lock()
	s.Messages = append(s.Messages, ai.ChatMessage{
		Role:    ai.RoleAssistant,
		Content: text,
	})
	s.mu.Unlock()

	s.hub.Broadcast(ServerMsg{
		Type:      MsgTypeEvent,
		Kind:      KindMessage,
		SessionID: s.ID,
		Role:      "assistant",
		Content:   text,
	})
}

// Abort cancels the active run.
func (s *Session) Abort() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancelFn != nil {
		log.Printf("[session] aborting run in %s", s.ID)
		s.cancelFn()
	}
}

// RequestApproval suspends the session run until the user responds.
func (s *Session) RequestApproval(command string) bool {
	id := fmt.Sprintf("appr-%d", time.Now().UnixNano())
	req := &ApprovalRequest{
		ID:        id,
		SessionID: s.ID,
		Command:   command,
		Response:  make(chan bool, 1),
	}

	s.approvalsMu.Lock()
	s.approvals[id] = req
	s.approvalsMu.Unlock()

	s.hub.Broadcast(ServerMsg{
		Type:       MsgTypeEvent,
		Kind:       KindApproval,
		SessionID:  s.ID,
		ApprovalID: id,
		Command:    command,
	})

	approved := <-req.Response
	return approved
}

// Approve responds to an approval request.
func (s *Session) Approve(approvalID string, approved bool) {
	s.approvalsMu.Lock()
	req, ok := s.approvals[approvalID]
	if ok {
		delete(s.approvals, approvalID)
	}
	s.approvalsMu.Unlock()

	if ok {
		req.Response <- approved
	}
}

// GetModel returns the session's current model name.
func (s *Session) GetModel() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Model
}

// UsageStats returns cumulative token usage for this session.
func (s *Session) UsageStats() UsageStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage
}

// ContextInfo returns context window information.
func (s *Session) ContextInfo() ContextInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Estimate ~4 chars per token as rough heuristic.
	totalChars := 0
	for _, m := range s.Messages {
		totalChars += len(m.Content)
	}
	return ContextInfo{
		Messages:        len(s.Messages),
		EstimatedTokens: totalChars / 4,
	}
}
