package control

// Message type constants for server→client messages.
const (
	MsgTypeEvent        = "event"
	MsgTypeSessions     = "sessions"
	MsgTypeError        = "error"
	MsgTypeConnected    = "connected"
	MsgTypeJobs         = "jobs"
	MsgTypeJobDetail    = "job_detail"
	MsgTypeJobEvents    = "job_events"
	MsgTypeJobArtifacts = "job_artifacts"
	MsgTypeWorkers      = "workers"
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
	CmdSend             = "send"
	CmdAbort            = "abort"
	CmdApprove          = "approve"
	CmdSetModel         = "set_model"
	CmdListSessions     = "list_sessions"
	CmdNewSession       = "new_session"
	CmdSwitch           = "switch_session"
	CmdSpawnSubagent    = "spawn_subagent"
	CmdBotCommand       = "bot_command" // slash commands routed directly to bot handlers
	CmdListJobs         = "list_jobs"
	CmdGetJob           = "get_job"
	CmdListJobEvents    = "list_job_events"
	CmdListJobArtifacts = "list_job_artifacts"
	CmdCancelJob        = "cancel_job"
	CmdListWorkers      = "list_workers"
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
	// Job dashboard fields.
	Jobs      []JobInfo      `json:"jobs,omitempty"`
	Job       *JobInfo       `json:"job,omitempty"`
	Events    []JobEventInfo `json:"events,omitempty"`
	Artifacts []ArtifactInfo `json:"artifacts,omitempty"`
	Workers   []WorkerInfo   `json:"workers,omitempty"`
}

// JobInfo is the JSON-friendly representation of a durable job for the dashboard.
type JobInfo struct {
	JobID              string `json:"job_id"`
	Kind               string `json:"kind"`
	Worker             string `json:"worker,omitempty"`
	SessionKey         string `json:"session_key,omitempty"`
	DeliverySessionKey string `json:"delivery_session_key,omitempty"`
	RetryOfJobID       string `json:"retry_of_job_id,omitempty"`
	Description        string `json:"description"`
	Status             string `json:"status"`
	CancelRequested    bool   `json:"cancel_requested,omitempty"`
	Attempt            int    `json:"attempt"`
	MaxAttempts        int    `json:"max_attempts"`
	TimeoutSeconds     int    `json:"timeout_seconds,omitempty"`
	Summary            string `json:"summary,omitempty"`
	Error              string `json:"error,omitempty"`
	CreatedAt          string `json:"created_at"`
	StartedAt          string `json:"started_at,omitempty"`
	CompletedAt        string `json:"completed_at,omitempty"`
	UpdatedAt          string `json:"updated_at"`
}

// JobEventInfo is the JSON-friendly representation of a job lifecycle event.
type JobEventInfo struct {
	ID        int64  `json:"id"`
	JobID     string `json:"job_id"`
	EventType string `json:"event_type"`
	Message   string `json:"message,omitempty"`
	Payload   string `json:"payload,omitempty"`
	CreatedAt string `json:"created_at"`
}

// ArtifactInfo is the JSON-friendly representation of a job artifact.
type ArtifactInfo struct {
	ID           int64  `json:"id"`
	JobID        string `json:"job_id"`
	Name         string `json:"name"`
	ArtifactType string `json:"artifact_type"`
	MimeType     string `json:"mime_type,omitempty"`
	Content      string `json:"content,omitempty"`
	URI          string `json:"uri,omitempty"`
	Metadata     string `json:"metadata,omitempty"`
	CreatedAt    string `json:"created_at"`
}

// WorkerInfo describes an active session worker for the dashboard.
type WorkerInfo struct {
	SessionKey string `json:"session_key"`
	Running    bool   `json:"running"`
	QueueDepth int    `json:"queue_depth"`
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
	// Image attachment (base64 data-URL, e.g. "data:image/png;base64,...").
	ImageData string `json:"image_data,omitempty"`
	// Sub-agent spawn fields (CmdSpawnSubagent).
	Task          string   `json:"task,omitempty"`
	Thinking      string   `json:"thinking,omitempty"`
	ToolAllowlist []string `json:"tool_allowlist,omitempty"`
	WorkspaceRoot string   `json:"workspace_root,omitempty"`
	MaxToolCalls  int      `json:"max_tool_calls,omitempty"`
	MaxDuration   string   `json:"max_duration,omitempty"`
	OutputFormat  string   `json:"output_format,omitempty"`
	OutputSchema  string   `json:"output_schema,omitempty"`
	MemoryPolicy  string   `json:"memory_policy,omitempty"`
	DeliverBack   bool     `json:"deliver_back,omitempty"`
	// Job dashboard fields.
	JobID string `json:"job_id,omitempty"`
	Limit int    `json:"limit,omitempty"`
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
