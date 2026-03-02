package control

import "encoding/json"

// Request types (client → server)
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

// Event types (server → client)
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

// Message is the wire format for all WebSocket messages.
// Requests from clients include an ID for correlation.
// Events pushed by the server have no ID.
type Message struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

// ErrorResponse wraps an error message for sending back to a client.
func ErrorResponse(id, reqType, msg string) Message {
	return Message{ID: id, Type: reqType, Error: msg}
}

// OKResponse wraps a successful payload for sending back to a client.
func OKResponse(id, reqType string, payload interface{}) (Message, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{ID: id, Type: reqType, Payload: json.RawMessage(b)}, nil
}

// NewEvent creates a server-push event message.
func NewEvent(evtType string, payload interface{}) (Message, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Message{}, err
	}
	return Message{Type: evtType, Payload: json.RawMessage(b)}, nil
}

// --- Request payload structs ---

// SessionSelectPayload is the payload for session.select requests.
type SessionSelectPayload struct {
	ChatID int64 `json:"chat_id"`
}

// ChatSendPayload is the payload for chat.send requests.
type ChatSendPayload struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

// RunAbortPayload is the payload for run.abort requests.
type RunAbortPayload struct {
	ChatID int64 `json:"chat_id"`
}

// AgentSetPayload is the payload for agent.set requests.
type AgentSetPayload struct {
	ChatID int64  `json:"chat_id"`
	Agent  string `json:"agent"`
}

// ModelSetPayload is the payload for model.set requests.
type ModelSetPayload struct {
	ChatID int64  `json:"chat_id"`
	Model  string `json:"model"`
}

// SubagentSpawnPayload is the payload for subagent.spawn requests.
type SubagentSpawnPayload struct {
	ParentChatID int64  `json:"parent_chat_id"`
	Task         string `json:"task"`
	Agent        string `json:"agent,omitempty"`
}

// ApprovalRespondPayload is the payload for approval.respond requests.
type ApprovalRespondPayload struct {
	ApprovalID string `json:"approval_id"`
	Approved   bool   `json:"approved"`
}

// --- Event payload structs ---

// SessionInfo describes an active session.
type SessionInfo struct {
	ChatID   int64  `json:"chat_id"`
	Username string `json:"username,omitempty"`
	State    string `json:"state"` // "idle", "running", "queued"
}

// RunDeltaPayload carries a streaming text chunk.
type RunDeltaPayload struct {
	ChatID int64  `json:"chat_id"`
	Delta  string `json:"delta"`
}

// ToolEventPayload describes a tool execution event.
type ToolEventPayload struct {
	ChatID   int64  `json:"chat_id"`
	ToolName string `json:"tool_name"`
	Input    string `json:"input,omitempty"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}

// RunEventPayload describes a run start/complete/fail event.
type RunEventPayload struct {
	ChatID int64  `json:"chat_id"`
	Error  string `json:"error,omitempty"`
}

// ApprovalRequestPayload carries details of a pending approval.
type ApprovalRequestPayload struct {
	ApprovalID string `json:"approval_id"`
	ChatID     int64  `json:"chat_id"`
	Command    string `json:"command"`
}

// ApprovalResolvedPayload carries the outcome of an approval.
type ApprovalResolvedPayload struct {
	ApprovalID string `json:"approval_id"`
	Approved   bool   `json:"approved"`
}
