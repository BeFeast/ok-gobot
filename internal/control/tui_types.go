package control

// Message type constants for server→client messages.
const (
	MsgTypeEvent     = "event"
	MsgTypeSessions  = "sessions"
	MsgTypeError     = "error"
	MsgTypeConnected = "connected"
)

// Event kind constants (embedded in MsgTypeEvent messages).
const (
	KindToken       = "token"
	KindMessage     = "message"
	KindToolStart   = "tool_start"
	KindToolEnd     = "tool_end"
	KindRunStart    = "run_start"
	KindRunEnd      = "run_end"
	KindError       = "error"
	KindApproval    = "approval_request"
	KindQueue       = "queue_update"
	KindChildDone   = "child_done"
	KindChildFailed = "child_failed"
)

// Command type constants for client→server messages.
const (
	CmdSend          = "send"
	CmdAbort         = "abort"
	CmdApprove       = "approve"
	CmdSetModel      = "set_model"
	CmdListSessions  = "list_sessions"
	CmdNewSession    = "new_session"
	CmdSwitch        = "switch_session"
	CmdSpawnSubagent = "spawn_subagent"
	CmdBotCommand    = "bot_command" // slash commands routed directly to bot handlers
)

// ServerMsg is sent from the control server to TUI clients.
type ServerMsg struct {
	Type             string           `json:"type"`
	Kind             string           `json:"kind,omitempty"`
	SessionID        string           `json:"session_id,omitempty"`
	Content          string           `json:"content,omitempty"`
	Timestamp        string           `json:"timestamp,omitempty"` // RFC3339 timestamp
	Role             string           `json:"role,omitempty"`
	Model            string           `json:"model,omitempty"`
	PromptTokens     int              `json:"prompt_tokens,omitempty"`
	CompletionTokens int              `json:"completion_tokens,omitempty"`
	TotalTokens      int              `json:"total_tokens,omitempty"`
	ToolName         string           `json:"tool_name,omitempty"`
	ToolArgs         string           `json:"tool_args,omitempty"`
	ToolResult       string           `json:"tool_result,omitempty"`
	ToolError        string           `json:"tool_error,omitempty"`
	ApprovalID       string           `json:"approval_id,omitempty"`
	Command          string           `json:"command,omitempty"`
	QueueDepth       int              `json:"queue_depth,omitempty"`
	Sessions         []TUISessionInfo `json:"sessions,omitempty"`
	Message          string           `json:"message,omitempty"`
	// Sub-agent spawn fields.
	ChildSessionKey string `json:"child_session_key,omitempty"`
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
	// Sub-agent spawn fields (CmdSpawnSubagent).
	Task          string   `json:"task,omitempty"`
	Thinking      string   `json:"thinking,omitempty"`
	ToolAllowlist []string `json:"tool_allowlist,omitempty"`
	WorkspaceRoot string   `json:"workspace_root,omitempty"`
	DeliverBack   bool     `json:"deliver_back,omitempty"`
}

// TUISessionInfo describes a session for the TUI session list.
type TUISessionInfo struct {
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
