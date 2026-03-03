package control

import "encoding/json"

const (
	ReqStatusGet       = "status.get"
	ReqSessionsList    = "sessions.list"
	ReqSessionSelect   = "session.select"
	ReqChatSend        = "chat.send"
	ReqRunAbort        = "run.abort"
	ReqAgentSet        = "agent.set"
	ReqModelSet        = "model.set"
	ReqSubagentSpawn   = "subagent.spawn"
	ReqApprovalRespond = "approval.respond"
)

const (
	EvtSessionAccepted  = "session.accepted"
	EvtSessionQueued    = "session.queued"
	EvtRunStarted       = "run.started"
	EvtRunDelta         = "run.delta"
	EvtToolStarted      = "tool.started"
	EvtToolFinished     = "tool.finished"
	EvtRunCompleted     = "run.completed"
	EvtRunFailed        = "run.failed"
	EvtApprovalRequest  = "approval.request"
	EvtApprovalResolved = "approval.resolved"
)

type Message struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func ErrorResponse(id, reqType, msg string) Message {
	return Message{ID: id, Type: reqType, Error: msg}
}

func OKResponse(id, reqType string, payload interface{}) (Message, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{ID: id, Type: reqType, Payload: json.RawMessage(b)}, nil
}

func NewEvent(evtType string, payload interface{}) (Message, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{Type: evtType, Payload: json.RawMessage(b)}, nil
}

type SessionSelectPayload struct {
	ChatID int64 `json:"chat_id"`
}

type ChatSendPayload struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

type RunAbortPayload struct {
	ChatID int64 `json:"chat_id"`
}

type AgentSetPayload struct {
	ChatID int64  `json:"chat_id"`
	Agent  string `json:"agent"`
}

type ModelSetPayload struct {
	ChatID int64  `json:"chat_id"`
	Model  string `json:"model"`
}

type SubagentSpawnPayload struct {
	ParentChatID int64  `json:"parent_chat_id"`
	Task         string `json:"task"`
	Agent        string `json:"agent,omitempty"`
}

type ApprovalRespondPayload struct {
	ApprovalID string `json:"approval_id"`
	Approved   bool   `json:"approved"`
}

type SessionInfo struct {
	ChatID   int64  `json:"chat_id"`
	Username string `json:"username,omitempty"`
	State    string `json:"state"`
}

type RunDeltaPayload struct {
	ChatID int64  `json:"chat_id"`
	Delta  string `json:"delta"`
}

type ToolEventPayload struct {
	ChatID   int64  `json:"chat_id"`
	ToolName string `json:"tool_name"`
	Input    string `json:"input,omitempty"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}

type RunEventPayload struct {
	ChatID int64  `json:"chat_id"`
	Error  string `json:"error,omitempty"`
}

type ApprovalRequestPayload struct {
	ApprovalID string `json:"approval_id"`
	ChatID     int64  `json:"chat_id"`
	Command    string `json:"command"`
}

type ApprovalResolvedPayload struct {
	ApprovalID string `json:"approval_id"`
	Approved   bool   `json:"approved"`
}
