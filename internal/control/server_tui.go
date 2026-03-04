package control

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"

	runtimepkg "ok-gobot/internal/runtime"
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
		CmdSpawnSubagent:
		return true
	default:
		return false
	}
}

func (s *Server) initTUIRuntime(ctx context.Context) {
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
		c.sendTUIError("new_session is not supported on bot control sessions")

	case CmdSend:
		sessionID, chatID, ok := s.resolveTUISession(c, cmd.SessionID, sessions)
		if !ok {
			return
		}
		text := strings.TrimSpace(cmd.Text)
		if text == "" {
			c.sendTUIError("text is required")
			return
		}
		s.hub.BroadcastTUI(ServerMsg{
			Type:      MsgTypeEvent,
			Kind:      KindMessage,
			SessionID: sessionID,
			Role:      "user",
			Content:   text,
		})
		if err := s.state.SendChat(chatID, text); err != nil {
			c.sendTUIError(err.Error())
			return
		}
		c.tuiSessionID = sessionID

	case CmdAbort:
		_, chatID, ok := s.resolveTUISession(c, cmd.SessionID, sessions)
		if !ok {
			return
		}
		if err := s.state.AbortRun(chatID); err != nil {
			c.sendTUIError(err.Error())
			return
		}

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
		sessionID, chatID, ok := s.resolveTUISession(c, cmd.SessionID, sessions)
		if !ok {
			return
		}
		model := strings.TrimSpace(cmd.Model)
		if model == "" {
			c.sendTUIError("model is required")
			return
		}
		if err := s.state.SetModel(chatID, model); err != nil {
			c.sendTUIError(err.Error())
			return
		}
		c.tuiSessionID = sessionID
		updated, err := s.listTUISessions()
		if err != nil {
			c.sendTUIError(err.Error())
			return
		}
		c.sendTUIMsg(ServerMsg{
			Type:     MsgTypeSessions,
			Sessions: updated,
		})

	case CmdSpawnSubagent:
		sessionID, _, ok := s.resolveTUISession(c, cmd.SessionID, sessions)
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

func (s *Server) ensureTUIConnected(c *client, requestedID string) ([]TUISessionInfo, bool) {
	sessions, err := s.listTUISessions()
	if err != nil {
		c.sendTUIError(err.Error())
		return nil, false
	}

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

func (s *Server) resolveTUISession(c *client, requestedID string, sessions []TUISessionInfo) (string, int64, bool) {
	sessionID := selectSessionID(sessions, requestedID, c.tuiSessionID)
	if sessionID == "" {
		c.sendTUIError("no sessions available")
		return "", 0, false
	}
	chatID, err := strconv.ParseInt(sessionID, 10, 64)
	if err != nil {
		c.sendTUIError("invalid session_id")
		return "", 0, false
	}
	if !hasSessionID(sessions, sessionID) {
		c.sendTUIError("session not found")
		return "", 0, false
	}
	c.tuiSessionID = sessionID
	return sessionID, chatID, true
}

func (s *Server) listTUISessions() ([]TUISessionInfo, error) {
	sessions, err := s.state.ListSessions()
	if err != nil {
		return nil, err
	}
	out := make([]TUISessionInfo, 0, len(sessions))
	for _, sess := range sessions {
		name := strings.TrimSpace(sess.Username)
		if name == "" {
			name = fmt.Sprintf("Chat %d", sess.ChatID)
		}
		out = append(out, TUISessionInfo{
			ID:      sessionIDForChat(sess.ChatID),
			Name:    name,
			Model:   sess.Model,
			Running: sess.State == "running" || sess.State == "queued",
		})
	}
	return out, nil
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
