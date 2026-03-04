package control

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	runtimepkg "ok-gobot/internal/runtime"
)

const (
	defaultTUISessionID   = "main"
	defaultTUISessionName = "Main"
	tuiSessionKeyPrefix   = "agent:default:tui:"
)

func isTUICommand(cmdType string) bool {
	switch cmdType {
	case CmdSend,
		CmdAbort,
		CmdApprove,
		CmdSetModel,
		CmdListSessions,
		CmdNewSession,
		CmdSwitch,
		CmdSpawnSubagent,
		CmdBotCommand:
		return true
	default:
		return false
	}
}

func (s *Server) initTUIRuntime(ctx context.Context) {
	s.ensureDefaultTUISession()
	s.runtimeHub = runtimepkg.NewHub(ctx, 64)
	evCh := make(chan runtimepkg.RuntimeEvent, 128)
	s.runtimeHub.Subscribe(evCh)
	go s.bridgeRuntimeEvents(ctx, evCh)
}

func (s *Server) handleTUIRequest(c *client, cmd ClientMsg) {
	sessions, ok := s.ensureTUIConnected(c, cmd.SessionID)
	if !ok {
		return
	}

	switch cmd.Type {
	case CmdListSessions:
		c.sendTUIMsg(ServerMsg{
			Type:     MsgTypeSessions,
			Sessions: sessions,
		})

	case CmdSwitch:
		target := strings.TrimSpace(cmd.SessionID)
		if target == "" {
			c.sendTUIError("session_id is required")
			return
		}
		if !hasSessionID(sessions, target) {
			c.sendTUIError("session not found")
			return
		}
		c.tuiSessionID = target
		c.sendTUIMsg(ServerMsg{
			Type:      MsgTypeConnected,
			SessionID: c.tuiSessionID,
			Sessions:  sessions,
		})

	case CmdNewSession:
		created := s.newTUISession(cmd.Name, cmd.Model)
		c.tuiSessionID = created.ID
		c.sendTUIMsg(ServerMsg{
			Type:      MsgTypeConnected,
			SessionID: created.ID,
			Sessions:  s.listTUISessions(),
		})

	case CmdSend:
		sessionID, ok := s.resolveTUISession(c, cmd.SessionID, sessions)
		if !ok {
			return
		}
		text := strings.TrimSpace(cmd.Text)
		if text == "" {
			c.sendTUIError("text is required")
			return
		}

		provider, ok := s.state.(TUIRunProvider)
		if !ok {
			c.sendTUIError("tui runtime provider not configured")
			return
		}

		snapshot, err := s.startTUIRun(sessionID)
		if err != nil {
			c.sendTUIError(err.Error())
			return
		}

		s.hub.BroadcastTUI(ServerMsg{
			Type:      MsgTypeEvent,
			Kind:      KindMessage,
			SessionID: sessionID,
			Role:      "user",
			Content:   text,
		})
		s.hub.BroadcastTUI(ServerMsg{
			Type:      MsgTypeEvent,
			Kind:      KindRunStart,
			SessionID: sessionID,
		})

		// Append user message to history before the run
		s.appendTUIHistory(sessionID, ai.ChatMessage{Role: ai.RoleUser, Content: text})

		req := TUIRunRequest{
			SessionKey: tuiSessionKeyForID(sessionID),
			Content:    text,
			Session:    snapshot.lastAssistant,
			History:    snapshot.history,
			Model:      snapshot.modelOverride,
			OnDelta: func(delta string) {
				if delta == "" {
					return
				}
				s.hub.BroadcastTUI(ServerMsg{
					Type:      MsgTypeEvent,
					Kind:      KindToken,
					SessionID: sessionID,
					Content:   delta,
				})
			},
			OnToolEvent: func(event agent.ToolEvent) {
				switch event.Type {
				case agent.ToolEventStarted:
					s.hub.BroadcastTUI(ServerMsg{
						Type:      MsgTypeEvent,
						Kind:      KindToolStart,
						SessionID: sessionID,
						ToolName:  event.ToolName,
						ToolArgs:  event.Input,
					})
				case agent.ToolEventFinished:
					msg := ServerMsg{
						Type:       MsgTypeEvent,
						Kind:       KindToolEnd,
						SessionID:  sessionID,
						ToolName:   event.ToolName,
						ToolResult: event.Output,
					}
					if event.Err != nil {
						msg.ToolError = event.Err.Error()
					}
					s.hub.BroadcastTUI(msg)
				}
			},
		}

		events := provider.SubmitTUIRun(context.Background(), req)
		if events == nil {
			s.finishTUIRun(sessionID, "")
			s.hub.BroadcastTUI(ServerMsg{
				Type:      MsgTypeEvent,
				Kind:      KindError,
				SessionID: sessionID,
				Message:   "tui runtime returned no events",
			})
			s.hub.BroadcastTUI(ServerMsg{
				Type:      MsgTypeEvent,
				Kind:      KindRunEnd,
				SessionID: sessionID,
			})
			return
		}
		go s.consumeTUIRunEvents(sessionID, text, events)
		c.tuiSessionID = sessionID

	case CmdAbort:
		sessionID, ok := s.resolveTUISession(c, cmd.SessionID, sessions)
		if !ok {
			return
		}
		provider, ok := s.state.(TUIRunProvider)
		if !ok {
			c.sendTUIError("tui runtime provider not configured")
			return
		}
		provider.AbortTUIRun(tuiSessionKeyForID(sessionID))

	case CmdApprove:
		if strings.TrimSpace(cmd.ApprovalID) == "" {
			c.sendTUIError("approval_id is required")
			return
		}
		if err := s.state.RespondToApproval(cmd.ApprovalID, cmd.Approved); err != nil {
			c.sendTUIError(err.Error())
			return
		}

	case CmdSetModel:
		sessionID, ok := s.resolveTUISession(c, cmd.SessionID, sessions)
		if !ok {
			return
		}
		model := strings.TrimSpace(cmd.Model)
		if model == "" {
			c.sendTUIError("model is required")
			return
		}
		if err := s.setTUIModel(sessionID, model); err != nil {
			c.sendTUIError(err.Error())
			return
		}
		c.tuiSessionID = sessionID
		c.sendTUIMsg(ServerMsg{
			Type:     MsgTypeSessions,
			Sessions: s.listTUISessions(),
		})

	case CmdBotCommand:
		s.handleTUIBotCommand(c, cmd)

	case CmdSpawnSubagent:
		sessionID, ok := s.resolveTUISession(c, cmd.SessionID, sessions)
		if !ok {
			return
		}
		task := strings.TrimSpace(cmd.Task)
		if task == "" {
			c.sendTUIError("task is required")
			return
		}
		if s.runtimeHub == nil {
			c.sendTUIError("runtime hub not ready")
			return
		}

		parentKey := "agent:tui:" + sessionID
		req := runtimepkg.SubagentSpawnRequest{
			ParentSessionKey: parentKey,
			Task:             task,
			Model:            cmd.Model,
			Thinking:         cmd.Thinking,
			ToolAllowlist:    cmd.ToolAllowlist,
			WorkspaceRoot:    cmd.WorkspaceRoot,
			DeliverBack:      true,
		}

		handle, err := s.runtimeHub.SpawnSubagent(req, func(ctx context.Context, ack runtimepkg.AckHandle) {
			log.Printf("[control] subagent started for session %s: %q", sessionID, task)
			ack.Close(nil)
		})
		if err != nil {
			c.sendTUIError(fmt.Sprintf("spawn subagent: %v", err))
			return
		}

		c.sendTUIMsg(ServerMsg{
			Type:            MsgTypeEvent,
			Kind:            KindRunStart,
			SessionID:       sessionID,
			ChildSessionKey: handle.SessionKey,
		})

	default:
		c.sendTUIError("unknown command type: " + cmd.Type)
	}
}

// handleTUIBotCommand executes a bot slash command directly and returns the result to the TUI.
func (s *Server) handleTUIBotCommand(c *client, cmd ClientMsg) {
	sessionID, ok := s.resolveTUISession(c, cmd.SessionID, s.listTUISessions())
	if !ok {
		return
	}

	text := strings.TrimSpace(cmd.Text)
	result := s.executeBotCommand(text)

	// Deliver as a synthetic assistant message
	c.sendTUIMsg(ServerMsg{
		Type:      MsgTypeEvent,
		Kind:      KindMessage,
		SessionID: sessionID,
		Role:      "assistant",
		Content:   result,
	})
}

// executeBotCommand runs a slash command and returns the text result.
func (s *Server) executeBotCommand(text string) string {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "unknown command"
	}
	cmd := strings.ToLower(strings.TrimPrefix(parts[0], "/"))

	switch cmd {
	case "status":
		return s.buildStatusText()
	case "commands", "help":
		return `🦞 *Available commands*

*Bot commands (handled directly):*
/status    — bot status, model, uptime
/usage     — token usage stats
/context   — context window info
/whoami    — your user info
/commands  — this list

*Session commands (sent to AI):*
/think <off|low|medium|high> — set thinking level
/verbose   — toggle verbose tool output
/compact   — compact context window
/new       — start new session
/abort     — abort active run

*TUI shortcuts:*
Ctrl+P     — session picker
Ctrl+M     — model picker
Ctrl+A     — abort run
Ctrl+N     — spawn sub-agent
Alt+Enter  — newline in input`
	default:
		return fmt.Sprintf("Unknown command: /%s\nType /commands to see available commands.", cmd)
	}
}

// buildStatusText formats a status string from the state provider.
func (s *Server) buildStatusText() string {
	status := s.state.GetStatus()
	if status == nil {
		return "⚠️ Status unavailable"
	}

	var sb strings.Builder
	sb.WriteString("🦞 *ok-gobot status*\n\n")

	if ai, ok := status["ai"].(map[string]interface{}); ok {
		if model, ok := ai["model"].(string); ok {
			sb.WriteString(fmt.Sprintf("🧠 Model: %s\n", model))
		}
		if provider, ok := ai["provider"].(string); ok {
			sb.WriteString(fmt.Sprintf("☁️  Provider: %s\n", provider))
		}
	} else if ai, ok := status["ai"].(map[string]string); ok {
		if model := ai["model"]; model != "" {
			sb.WriteString(fmt.Sprintf("🧠 Model: %s\n", model))
		}
	}

	if uptime, ok := status["uptime"].(string); ok {
		sb.WriteString(fmt.Sprintf("⏱  Uptime: %s\n", uptime))
	}

	if v, ok := status["status"].(string); ok {
		sb.WriteString(fmt.Sprintf("🟢 Status: %s\n", v))
	}

	return sb.String()
}

func (s *Server) ensureTUIConnected(c *client, requestedID string) ([]TUISessionInfo, bool) {
	sessions := s.listTUISessions()

	if !c.tuiConnected {
		c.tuiConnected = true
		c.tuiSessionID = selectSessionID(sessions, requestedID, c.tuiSessionID)
		c.sendTUIMsg(ServerMsg{
			Type:      MsgTypeConnected,
			SessionID: c.tuiSessionID,
			Sessions:  sessions,
		})
	}

	return sessions, true
}

func (s *Server) resolveTUISession(c *client, requestedID string, sessions []TUISessionInfo) (string, bool) {
	sessionID := selectSessionID(sessions, requestedID, c.tuiSessionID)
	if sessionID == "" {
		c.sendTUIError("no sessions available")
		return "", false
	}
	if !hasSessionID(sessions, sessionID) {
		c.sendTUIError("session not found")
		return "", false
	}
	c.tuiSessionID = sessionID
	return sessionID, true
}

func (s *Server) consumeTUIRunEvents(sessionID, userText string, events <-chan agent.RunEvent) {
	var finalMessage string
	defer func() {
		s.finishTUIRun(sessionID, finalMessage)
		// Log the exchange if the provider supports it
		if logger, ok := s.state.(TUIRunProvider); ok {
			logger.LogTUIExchange(userText, finalMessage)
		}
		if strings.TrimSpace(finalMessage) != "" {
			s.hub.BroadcastTUI(ServerMsg{
				Type:      MsgTypeEvent,
				Kind:      KindMessage,
				SessionID: sessionID,
				Role:      "assistant",
				Content:   finalMessage,
			})
		}
		s.hub.BroadcastTUI(ServerMsg{
			Type:      MsgTypeEvent,
			Kind:      KindRunEnd,
			SessionID: sessionID,
		})
	}()

	for ev := range events {
		switch ev.Type {
		case agent.RunEventDone:
			if ev.Result != nil {
				finalMessage = ev.Result.Message
			}
		case agent.RunEventError:
			if ev.Err != nil && !errors.Is(ev.Err, context.Canceled) {
				s.hub.BroadcastTUI(ServerMsg{
					Type:      MsgTypeEvent,
					Kind:      KindError,
					SessionID: sessionID,
					Message:   ev.Err.Error(),
				})
			}
		}
	}
}

func (s *Server) ensureDefaultTUISession() {
	s.tuiMu.Lock()
	defer s.tuiMu.Unlock()

	if s.tuiState == nil {
		s.tuiState = &tuiSessionStore{byID: make(map[string]*tuiSessionState)}
	}
	if len(s.tuiState.byID) > 0 {
		return
	}

	s.tuiState.byID[defaultTUISessionID] = &tuiSessionState{
		ID:        defaultTUISessionID,
		Name:      defaultTUISessionName,
		Model:     s.defaultTUIModel(),
		CreatedAt: time.Now(),
	}
	s.tuiState.order = []string{defaultTUISessionID}
}

func (s *Server) newTUISession(name, model string) TUISessionInfo {
	s.ensureDefaultTUISession()

	s.tuiMu.Lock()
	defer s.tuiMu.Unlock()

	for {
		s.tuiState.nextID++
		id := fmt.Sprintf("tui-%d", s.tuiState.nextID)
		if _, exists := s.tuiState.byID[id]; exists {
			continue
		}
		displayName := strings.TrimSpace(name)
		if displayName == "" {
			displayName = fmt.Sprintf("Chat %d", len(s.tuiState.order)+1)
		}
		displayModel := strings.TrimSpace(model)
		if displayModel == "" {
			displayModel = s.defaultTUIModel()
		}
		session := &tuiSessionState{
			ID:            id,
			Name:          displayName,
			Model:         displayModel,
			ModelOverride: strings.TrimSpace(model),
			CreatedAt:     time.Now(),
		}
		s.tuiState.byID[id] = session
		s.tuiState.order = append(s.tuiState.order, id)
		return TUISessionInfo{
			ID:      session.ID,
			Name:    session.Name,
			Model:   session.Model,
			Running: session.Running,
		}
	}
}

func (s *Server) listTUISessions() []TUISessionInfo {
	s.ensureDefaultTUISession()

	s.tuiMu.Lock()
	defer s.tuiMu.Unlock()

	out := make([]TUISessionInfo, 0, len(s.tuiState.order))
	for _, id := range s.tuiState.order {
		session, ok := s.tuiState.byID[id]
		if !ok {
			continue
		}
		out = append(out, TUISessionInfo{
			ID:      session.ID,
			Name:    session.Name,
			Model:   session.Model,
			Running: session.Running,
		})
	}
	return out
}

func (s *Server) setTUIModel(sessionID, model string) error {
	s.tuiMu.Lock()
	defer s.tuiMu.Unlock()

	session, ok := s.tuiState.byID[sessionID]
	if !ok {
		return fmt.Errorf("session not found")
	}
	session.Model = model
	session.ModelOverride = model
	return nil
}

type tuiRunSnapshot struct {
	lastAssistant string
	modelOverride string
	history       []ai.ChatMessage
}

func (s *Server) startTUIRun(sessionID string) (tuiRunSnapshot, error) {
	s.tuiMu.Lock()
	defer s.tuiMu.Unlock()

	session, ok := s.tuiState.byID[sessionID]
	if !ok {
		return tuiRunSnapshot{}, fmt.Errorf("session not found")
	}
	if session.Running {
		return tuiRunSnapshot{}, fmt.Errorf("A run is already in progress. Use /abort first.")
	}
	session.Running = true
	// snapshot history (copy slice header; elements are immutable)
	hist := make([]ai.ChatMessage, len(session.History))
	copy(hist, session.History)
	return tuiRunSnapshot{
		lastAssistant: session.LastAssistant,
		modelOverride: session.ModelOverride,
		history:       hist,
	}, nil
}

func (s *Server) finishTUIRun(sessionID, assistant string) {
	s.tuiMu.Lock()
	defer s.tuiMu.Unlock()

	session, ok := s.tuiState.byID[sessionID]
	if !ok {
		return
	}
	session.Running = false
	if strings.TrimSpace(assistant) != "" {
		session.LastAssistant = assistant
		session.History = append(session.History, ai.ChatMessage{Role: ai.RoleAssistant, Content: assistant})
	}
}

func (s *Server) appendTUIHistory(sessionID string, msg ai.ChatMessage) {
	s.tuiMu.Lock()
	defer s.tuiMu.Unlock()
	if session, ok := s.tuiState.byID[sessionID]; ok {
		session.History = append(session.History, msg)
	}
}

func (s *Server) defaultTUIModel() string {
	status := s.state.GetStatus()
	if status == nil {
		return ""
	}

	aiRaw, ok := status["ai"]
	if !ok {
		return ""
	}

	switch aiMap := aiRaw.(type) {
	case map[string]string:
		return strings.TrimSpace(aiMap["model"])
	case map[string]interface{}:
		if model, ok := aiMap["model"].(string); ok {
			return strings.TrimSpace(model)
		}
	}
	return ""
}

func hasSessionID(sessions []TUISessionInfo, id string) bool {
	for _, s := range sessions {
		if s.ID == id {
			return true
		}
	}
	return false
}

func selectSessionID(sessions []TUISessionInfo, requestedID, fallbackID string) string {
	req := strings.TrimSpace(requestedID)
	if req != "" && hasSessionID(sessions, req) {
		return req
	}
	fallback := strings.TrimSpace(fallbackID)
	if fallback != "" && hasSessionID(sessions, fallback) {
		return fallback
	}
	if len(sessions) > 0 {
		return sessions[0].ID
	}
	return ""
}

func tuiSessionKeyForID(sessionID string) string {
	return tuiSessionKeyPrefix + sessionID
}

func (s *Server) bridgeRuntimeEvents(ctx context.Context, evCh <-chan runtimepkg.RuntimeEvent) {
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
			if !strings.HasPrefix(ev.SessionKey, parentPrefix) {
				continue
			}

			sessionID := strings.TrimPrefix(ev.SessionKey, parentPrefix)
			payload, ok := ev.Payload.(runtimepkg.ChildCompletionPayload)
			if !ok {
				continue
			}

			kind := KindChildDone
			msg := ""
			if ev.Type == runtimepkg.EventChildFailed {
				kind = KindChildFailed
				if payload.Err != nil {
					msg = payload.Err.Error()
				}
			}

			s.hub.BroadcastTUI(ServerMsg{
				Type:            MsgTypeEvent,
				Kind:            kind,
				SessionID:       sessionID,
				ChildSessionKey: payload.ChildSessionKey,
				Message:         msg,
			})
		}
	}
}
