package control

import (
	"fmt"
	"strconv"
)

// legacyEventToTUI converts legacy control events into TUI websocket messages.
func legacyEventToTUI(evtType string, payload interface{}) []ServerMsg {
	switch evtType {
	case EvtRunStarted:
		p, ok := asRunEventPayload(payload)
		if !ok {
			return nil
		}
		return []ServerMsg{{
			Type:      MsgTypeEvent,
			Kind:      KindRunStart,
			SessionID: sessionIDForChat(p.ChatID),
		}}
	case EvtRunDelta:
		p, ok := asRunDeltaPayload(payload)
		if !ok {
			return nil
		}
		return []ServerMsg{{
			Type:      MsgTypeEvent,
			Kind:      KindToken,
			SessionID: sessionIDForChat(p.ChatID),
			Content:   p.Delta,
		}}
	case EvtRunCompleted:
		p, ok := asRunEventPayload(payload)
		if !ok {
			return nil
		}
		return []ServerMsg{{
			Type:      MsgTypeEvent,
			Kind:      KindRunEnd,
			SessionID: sessionIDForChat(p.ChatID),
		}}
	case EvtRunFailed:
		p, ok := asRunEventPayload(payload)
		if !ok {
			return nil
		}
		msgs := make([]ServerMsg, 0, 2)
		if p.Error != "" {
			msgs = append(msgs, ServerMsg{
				Type:      MsgTypeEvent,
				Kind:      KindError,
				SessionID: sessionIDForChat(p.ChatID),
				Message:   p.Error,
			})
		}
		msgs = append(msgs, ServerMsg{
			Type:      MsgTypeEvent,
			Kind:      KindRunEnd,
			SessionID: sessionIDForChat(p.ChatID),
		})
		return msgs
	case EvtToolStarted:
		p, ok := asToolEventPayload(payload)
		if !ok {
			return nil
		}
		return []ServerMsg{{
			Type:      MsgTypeEvent,
			Kind:      KindToolStart,
			SessionID: sessionIDForChat(p.ChatID),
			ToolName:  p.ToolName,
			ToolArgs:  p.Input,
		}}
	case EvtToolFinished:
		p, ok := asToolEventPayload(payload)
		if !ok {
			return nil
		}
		return []ServerMsg{{
			Type:       MsgTypeEvent,
			Kind:       KindToolEnd,
			SessionID:  sessionIDForChat(p.ChatID),
			ToolName:   p.ToolName,
			ToolResult: p.Output,
			ToolError:  p.Error,
		}}
	case EvtToolDenied:
		p, ok := asToolDeniedPayload(payload)
		if !ok {
			return nil
		}
		errMsg := fmt.Sprintf("\U0001F6AB Tool \"%s\" is disabled (%s). Run `%s` to re-enable.", p.ToolName, p.Reason, p.ReEnable)
		return []ServerMsg{{
			Type:      MsgTypeEvent,
			Kind:      KindToolEnd,
			SessionID: sessionIDForChat(p.ChatID),
			ToolName:  p.ToolName,
			ToolError: errMsg,
		}}
	case EvtApprovalRequest:
		p, ok := asApprovalRequestPayload(payload)
		if !ok {
			return nil
		}
		return []ServerMsg{{
			Type:       MsgTypeEvent,
			Kind:       KindApproval,
			SessionID:  sessionIDForChat(p.ChatID),
			ApprovalID: p.ApprovalID,
			Command:    p.Command,
		}}
	case EvtSessionQueued:
		p, ok := asSessionInfo(payload)
		if !ok {
			return nil
		}
		return []ServerMsg{{
			Type:       MsgTypeEvent,
			Kind:       KindQueue,
			SessionID:  sessionIDForChat(p.ChatID),
			QueueDepth: 1,
		}}
	default:
		return nil
	}
}

func asRunEventPayload(payload interface{}) (RunEventPayload, bool) {
	switch p := payload.(type) {
	case RunEventPayload:
		return p, true
	case *RunEventPayload:
		if p == nil {
			return RunEventPayload{}, false
		}
		return *p, true
	default:
		return RunEventPayload{}, false
	}
}

func asRunDeltaPayload(payload interface{}) (RunDeltaPayload, bool) {
	switch p := payload.(type) {
	case RunDeltaPayload:
		return p, true
	case *RunDeltaPayload:
		if p == nil {
			return RunDeltaPayload{}, false
		}
		return *p, true
	default:
		return RunDeltaPayload{}, false
	}
}

func asToolEventPayload(payload interface{}) (ToolEventPayload, bool) {
	switch p := payload.(type) {
	case ToolEventPayload:
		return p, true
	case *ToolEventPayload:
		if p == nil {
			return ToolEventPayload{}, false
		}
		return *p, true
	default:
		return ToolEventPayload{}, false
	}
}

func asApprovalRequestPayload(payload interface{}) (ApprovalRequestPayload, bool) {
	switch p := payload.(type) {
	case ApprovalRequestPayload:
		return p, true
	case *ApprovalRequestPayload:
		if p == nil {
			return ApprovalRequestPayload{}, false
		}
		return *p, true
	default:
		return ApprovalRequestPayload{}, false
	}
}

func asSessionInfo(payload interface{}) (SessionInfo, bool) {
	switch p := payload.(type) {
	case SessionInfo:
		return p, true
	case *SessionInfo:
		if p == nil {
			return SessionInfo{}, false
		}
		return *p, true
	default:
		return SessionInfo{}, false
	}
}

func asToolDeniedPayload(payload interface{}) (ToolDeniedPayload, bool) {
	switch p := payload.(type) {
	case ToolDeniedPayload:
		return p, true
	case *ToolDeniedPayload:
		if p == nil {
			return ToolDeniedPayload{}, false
		}
		return *p, true
	default:
		return ToolDeniedPayload{}, false
	}
}

func sessionIDForChat(chatID int64) string {
	return strconv.FormatInt(chatID, 10)
}
