// Package tui provides a Bubble Tea terminal UI for ok-gobot.
package tui

import "time"

// Message types from server -> client.
const (
	MsgTypeToken       = "token"        // streaming token
	MsgTypeMessage     = "message"      // complete message
	MsgTypeToolEvent   = "tool_event"   // tool call start/end
	MsgTypeSessionList = "session_list" // list of sessions
	MsgTypeAgentList   = "agent_list"   // available agents
	MsgTypeApprovalReq = "approval_req" // approval needed
	MsgTypeRunState    = "run_state"    // idle/running/waiting
	MsgTypeQueueDepth  = "queue_depth"  // pending items count
	MsgTypeSpawnDialog = "spawn_dialog" // spawn sub-agent
	MsgTypeModelList   = "model_list"   // available models
	MsgTypeError       = "error"        // error from server
)

// Command types from client -> server.
const (
	CmdSend          = "send"           // send user message
	CmdSwitchSession = "switch_session" // switch active session
	CmdSetModel      = "set_model"      // override model
	CmdSetAgent      = "set_agent"      // switch agent
	CmdAbort         = "abort"          // cancel active run
	CmdApprove       = "approve"        // approve/reject action
	CmdSpawnAgent    = "spawn_agent"    // spawn sub-agent
	CmdListSessions  = "list_sessions"  // request session list
	CmdListAgents    = "list_agents"    // request agent list
	CmdListModels    = "list_models"    // request model list
)

// ServerMessage is a message received from the control server.
type ServerMessage struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`

	// token / message
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`

	// tool_event
	Tool   string      `json:"tool,omitempty"`
	Status string      `json:"status,omitempty"` // start / end / error
	Input  interface{} `json:"input,omitempty"`
	Output interface{} `json:"output,omitempty"`

	// session_list
	Sessions []SessionInfo `json:"sessions,omitempty"`

	// agent_list
	Agents []AgentInfo `json:"agents,omitempty"`

	// model_list
	Models []string `json:"models,omitempty"`

	// approval_req
	ApprovalID string `json:"approval_id,omitempty"`
	Command    string `json:"command,omitempty"`

	// run_state
	State string `json:"state,omitempty"` // idle / running / waiting_approval

	// queue_depth
	Depth int `json:"depth,omitempty"`

	// error
	Error string `json:"error,omitempty"`
}

// ClientCommand is a command sent to the control server.
type ClientCommand struct {
	Type       string `json:"type"`
	SessionID  string `json:"session_id,omitempty"`
	Content    string `json:"content,omitempty"`
	Model      string `json:"model,omitempty"`
	Agent      string `json:"agent,omitempty"`
	ApprovalID string `json:"approval_id,omitempty"`
	Approved   bool   `json:"approved,omitempty"`
	Name       string `json:"name,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
}

// SessionInfo holds metadata for a session.
type SessionInfo struct {
	ID          string    `json:"id"`
	ChatID      int64     `json:"chat_id"`
	Name        string    `json:"name"`
	Model       string    `json:"model"`
	ActiveAgent string    `json:"active_agent"`
	State       string    `json:"state"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AgentInfo holds metadata for an agent.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
