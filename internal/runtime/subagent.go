package runtime

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"

	"ok-gobot/internal/session"
)

// SubagentSpawnRequest carries all parameters needed to spawn a sub-agent run.
type SubagentSpawnRequest struct {
	// ParentSessionKey is the canonical session key of the calling session.
	// Must follow the format: agent:<agentId>:<rest>
	ParentSessionKey string

	// Task is the task description sent to the sub-agent.
	Task string

	// Model is the model identifier to use for the sub-agent run.
	// Empty string means the sub-agent inherits the default model.
	Model string

	// Thinking controls the reasoning level: "off", "low", "medium", "high".
	Thinking string

	// ToolAllowlist is the set of tool names the sub-agent is permitted to call.
	// An empty slice means no restriction (all tools allowed).
	ToolAllowlist []string

	// WorkspaceRoot is the absolute path scoped to the sub-agent's workspace.
	WorkspaceRoot string

	// DeliverBack, when true, routes the sub-agent result back to the parent session.
	DeliverBack bool
}

// SubagentHandle holds the resolved identifiers for a successfully spawned sub-agent.
type SubagentHandle struct {
	// SessionKey is the canonical key assigned to the child session.
	// Format: agent:<agentId>:subagent:<runSlug>
	SessionKey string

	// RunSlug is the unique slug component used to construct SessionKey.
	RunSlug string

	// AgentID is the agent identifier extracted from the parent session key.
	AgentID string

	// Ack is the AckHandle returned by Hub.Submit for the child run.
	Ack AckHandle
}

// SpawnSubagent creates an isolated child session worker for req.
//
// It extracts the agentID from req.ParentSessionKey, generates a unique
// runSlug, constructs the canonical child session key
// (agent:<agentId>:subagent:<runSlug>), and submits run to the Hub.
//
// The caller is responsible for any persistence of the subagent run record;
// SpawnSubagent only manages the runtime lifecycle.
func (h *Hub) SpawnSubagent(req SubagentSpawnRequest, run RunFunc) (*SubagentHandle, error) {
	agentID, err := extractAgentID(req.ParentSessionKey)
	if err != nil {
		return nil, fmt.Errorf("runtime: SpawnSubagent: %w", err)
	}

	runSlug := newRunSlug()
	childKey := session.Subagent(agentID, runSlug)

	ack := h.Submit(childKey, runSlug, run)

	return &SubagentHandle{
		SessionKey: childKey,
		RunSlug:    runSlug,
		AgentID:    agentID,
		Ack:        ack,
	}, nil
}

// extractAgentID parses the agent ID from a canonical session key.
// Expected format: agent:<agentId>:<rest> or agent:<agentId>
func extractAgentID(sessionKey string) (string, error) {
	const prefix = "agent:"
	if !strings.HasPrefix(sessionKey, prefix) {
		return "", fmt.Errorf("session key must begin with %q, got %q", prefix, sessionKey)
	}
	rest := sessionKey[len(prefix):]
	if rest == "" {
		return "", fmt.Errorf("empty agent ID in session key %q", sessionKey)
	}
	idx := strings.Index(rest, ":")
	if idx == 0 {
		return "", fmt.Errorf("empty agent ID in session key %q", sessionKey)
	}
	if idx == -1 {
		return rest, nil
	}
	return rest[:idx], nil
}

// newRunSlug returns a unique slug using a nanosecond timestamp and 4 random bytes.
func newRunSlug() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%d-%x", time.Now().UnixNano(), b)
}
