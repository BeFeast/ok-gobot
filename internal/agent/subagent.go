package agent

import (
	"time"

	"ok-gobot/internal/delegation"
)

// SubagentSpawnRequest defines parameters for spawning a child sub-agent.
type SubagentSpawnRequest struct {
	Description  string        // Task description passed to the sub-agent
	Model        string        // Optional model override (empty = use caller's default)
	ThinkLevel   string        // Optional thinking level: off, low, medium, high
	MaxToolCalls int           // Optional explicit tool-call budget
	MaxDuration  time.Duration // Optional explicit max runtime
	OutputFormat string        // Expected output format: markdown, text, json
	OutputSchema string        // Optional output shape/schema hint
	MemoryPolicy string        // Memory write policy: inherit, read_only, allow_writes
}

// Job returns the normalized delegated-run contract for this spawn request.
func (r SubagentSpawnRequest) Job() delegation.Job {
	return delegation.Job{
		Model:        r.Model,
		Thinking:     r.ThinkLevel,
		MaxToolCalls: r.MaxToolCalls,
		MaxDuration:  r.MaxDuration,
		OutputFormat: r.OutputFormat,
		OutputSchema: r.OutputSchema,
		MemoryPolicy: r.MemoryPolicy,
	}.WithDefaults()
}

// SubagentResult holds the outcome of a completed sub-agent run.
type SubagentResult struct {
	Success bool
	Summary string
	Error   error
}
