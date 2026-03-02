package controlserver

// Message type constants for server→client messages.
const (
	MsgTypeEvent     = "event"
	MsgTypeSessions  = "sessions"
	MsgTypeError     = "error"
	MsgTypeConnected = "connected"
)

// Event kind constants (embedded in MsgTypeEvent messages).
const (
	KindToken     = "token"
	KindMessage   = "message"
	KindToolStart = "tool_start"
	KindToolEnd   = "tool_end"
	KindRunStart  = "run_start"
	KindRunEnd    = "run_end"
	KindError     = "error"
	KindApproval  = "approval_request"
	KindQueue     = "queue_update"
)

// Command type constants for client→server messages.
const (
	CmdSend         = "send"
	CmdAbort        = "abort"
	CmdApprove      = "approve"
	CmdSetModel     = "set_model"
	CmdListSessions = "list_sessions"
	CmdNewSession   = "new_session"
	CmdSwitch       = "switch_session"
)

// ServerMsg is sent from the control server to TUI clients.
type ServerMsg struct {
	Type       string        `json:"type"`
	Kind       string        `json:"kind,omitempty"`
	SessionID  string        `json:"session_id,omitempty"`
	Content    string        `json:"content,omitempty"`
	Role       string        `json:"role,omitempty"`
	ToolName   string        `json:"tool_name,omitempty"`
	ToolArgs   string        `json:"tool_args,omitempty"`
	ToolResult string        `json:"tool_result,omitempty"`
	ToolError  string        `json:"tool_error,omitempty"`
	ApprovalID string        `json:"approval_id,omitempty"`
	Command    string        `json:"command,omitempty"`
	QueueDepth int           `json:"queue_depth,omitempty"`
	Sessions   []SessionInfo `json:"sessions,omitempty"`
	Message    string        `json:"message,omitempty"`
}

// ClientMsg is sent from TUI clients to the control server.
type ClientMsg struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id,omitempty"`
	Text       string `json:"text,omitempty"`
	Model      string `json:"model,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	Approved   bool   `json:"approved"`
	Name       string `json:"name,omitempty"`
	Agent      string `json:"agent,omitempty"`
}

// SessionInfo describes a session for the session list.
type SessionInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Model   string `json:"model"`
	Running bool   `json:"running"`
}

// ApprovalRequest is a pending command approval.
type ApprovalRequest struct {
	ID        string
	SessionID string
	Command   string
	Response  chan bool
}
