package worker

import "context"

// Request describes one bounded worker task.
// Callers shape the task text; adapters normalize execution and result handling.
type Request struct {
	Task    string
	Model   string
	WorkDir string
}

// Result is the normalized final output from a worker backend.
type Result struct {
	Content   string
	SessionID string
}

// Event is a streamed update from a worker backend.
type Event struct {
	Content string
	Done    bool
	Error   error
}

// Adapter executes a task through a worker backend.
// Implementations must respect context cancellation and timeouts.
type Adapter interface {
	Run(ctx context.Context, req Request) (*Result, error)
	Stream(ctx context.Context, req Request) <-chan Event
}
