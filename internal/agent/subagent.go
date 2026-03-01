package agent

// SubagentSpawnRequest defines parameters for spawning a child sub-agent.
type SubagentSpawnRequest struct {
	Description string // Task description passed to the sub-agent
	Model       string // Optional model override (empty = use caller's default)
	ThinkLevel  string // Optional thinking level: off, low, medium, high
}

// SubagentResult holds the outcome of a completed sub-agent run.
type SubagentResult struct {
	Success bool
	Summary string
	Error   error
}
