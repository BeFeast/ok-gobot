package control

const (
	EvtSessionAccepted  = "session.accepted"
	EvtSessionQueued    = "session.queued"
	EvtRunStarted       = "run.started"
	EvtRunDelta         = "run.delta"
	EvtToolStarted      = "tool.started"
	EvtToolFinished     = "tool.finished"
	EvtRunCompleted     = "run.completed"
	EvtRunFailed        = "run.failed"
	EvtToolDenied       = "tool.denied"
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

type RunEventPayload struct {
	ChatID int64  `json:"chat_id"`
	Error  string `json:"error,omitempty"`
}

// ToolDeniedPayload carries structured denial information for tool.denied events.
type ToolDeniedPayload struct {
	ChatID      int64  `json:"chat_id"`
	ToolName    string `json:"tool_name"`
	Family      string `json:"family"`
	Reason      string `json:"reason"`
	Remediation string `json:"remediation,omitempty"`
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
