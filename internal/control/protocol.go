package control

const (
	EvtSessionAccepted  = "session.accepted"
	EvtSessionQueued    = "session.queued"
	EvtRunStarted       = "run.started"
	EvtRunDelta         = "run.delta"
	EvtToolStarted      = "tool.started"
	EvtToolFinished     = "tool.finished"
	EvtToolDenied       = "tool.denied"
	EvtRunCompleted     = "run.completed"
	EvtRunFailed        = "run.failed"
	EvtApprovalRequest  = "approval.request"
	EvtApprovalResolved = "approval.resolved"
)

type SessionInfo struct {
	ChatID   int64  `json:"chat_id"`
	Username string `json:"username,omitempty"`
	Model    string `json:"model,omitempty"`
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

// ToolDeniedPayload is emitted when a tool call is blocked by policy.
type ToolDeniedPayload struct {
	ChatID   int64  `json:"chat_id"`
	ToolName string `json:"tool_name"`
	Family   string `json:"family"`
	Reason   string `json:"reason"`
	ReEnable string `json:"re_enable"`
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
