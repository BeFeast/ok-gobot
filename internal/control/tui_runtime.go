package control

import (
	"context"
	"time"

	"ok-gobot/internal/agent"
	"ok-gobot/internal/ai"
	"ok-gobot/internal/delegation"
)

// TUIRunRequest describes one isolated TUI run routed through the bot runtime hub.
type TUIRunRequest struct {
	SessionKey   string
	Content      string
	UserContent  []ai.ContentBlock // optional multimodal blocks (e.g. image + text)
	Session      string            // legacy: last assistant text (kept for compat)
	History      []ai.ChatMessage  // full conversation history
	Model        string
	Job          *delegation.Job
	OnToolEvent  func(agent.ToolEvent)
	OnDelta      func(string)
	OnDeltaReset func()
}

// TUIRunProvider is an optional state extension used by the control server TUI
// command path. Implementations submit isolated TUI runs to the bot runtime hub.
type TUIRunProvider interface {
	SubmitTUIRun(ctx context.Context, req TUIRunRequest) <-chan agent.RunEvent
	AbortTUIRun(sessionKey string)
	// LogTUIExchange logs a user+assistant exchange from a TUI session.
	// Implementations may write to memory/store or no-op if unsupported.
	LogTUIExchange(userText, assistantText string)
	// GetStatusText returns a formatted status string identical to /status in Telegram.
	GetStatusText(sessionID string) string
}

type tuiSessionState struct {
	ID            string
	Name          string
	Model         string
	ModelOverride string
	LastAssistant string
	History       []ai.ChatMessage // full conversation history for context
	Running       bool
	CreatedAt     time.Time
}

type tuiSessionStore struct {
	byID   map[string]*tuiSessionState
	order  []string
	nextID int
}

// JobDataProvider gives the control server read access to durable job data.
// Implement on the app adapter and pass via SetJobDataProvider.
type JobDataProvider interface {
	ListJobs(limit int) ([]JobInfo, error)
	GetJob(jobID string) (*JobInfo, error)
	ListJobEvents(jobID string, limit int) ([]JobEventInfo, error)
	ListJobArtifacts(jobID string, limit int) ([]JobArtifactInfo, error)
}

// JobInfo is the wire-format summary of a durable job.
type JobInfo struct {
	JobID              string `json:"job_id"`
	Kind               string `json:"kind"`
	Worker             string `json:"worker"`
	SessionKey         string `json:"session_key"`
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

// JobEventInfo is the wire-format of one job lifecycle event.
type JobEventInfo struct {
	ID        int64  `json:"id"`
	JobID     string `json:"job_id"`
	EventType string `json:"event_type"`
	Message   string `json:"message"`
	Payload   string `json:"payload,omitempty"`
	CreatedAt string `json:"created_at"`
}

// JobArtifactInfo is the wire-format of one job artifact.
type JobArtifactInfo struct {
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
