package agent

import (
	"context"
	"fmt"
	"log"
	"sync"

	"ok-gobot/internal/ai"
)

// SessionKey is the canonical identifier for a chat session.
// Format: "dm:<chatID>" for private chats, "group:<chatID>" for groups.
type SessionKey string

// NewDMSessionKey returns the canonical session key for a private (DM) chat.
func NewDMSessionKey(chatID int64) SessionKey {
	return SessionKey(fmt.Sprintf("dm:%d", chatID))
}

// NewGroupSessionKey returns the canonical session key for a group/supergroup/channel.
func NewGroupSessionKey(chatID int64) SessionKey {
	return SessionKey(fmt.Sprintf("group:%d", chatID))
}

// RunEventType describes the kind of event emitted by the hub.
type RunEventType string

const (
	// RunEventDone signals a successful completion; Result is set.
	RunEventDone RunEventType = "done"
	// RunEventError signals a failure; Err is set.
	RunEventError RunEventType = "error"
)

// RunEvent is emitted by the RuntimeHub when a run completes or fails.
type RunEvent struct {
	Type        RunEventType
	Result      *AgentResponse // non-nil when Type == RunEventDone
	Err         error          // non-nil when Type == RunEventError
	ProfileName string         // agent profile that handled the run
}

// RunRequest carries everything the hub needs to execute an agent run.
// The hub owns agent creation via its RunResolver — callers no longer
// supply a pre-built ToolCallingAgent.
type RunRequest struct {
	SessionKey   SessionKey
	ChatID       int64
	Content      string
	UserContent  []ai.ContentBlock // optional multimodal user blocks (e.g. image + text)
	Session      string            // legacy: last assistant text (single turn)
	History      []ai.ChatMessage  // full conversation history (preferred over Session)
	Context      context.Context
	OnToolEvent  func(ToolEvent) // optional callback for tool status updates
	OnDelta      func(string)    // optional callback for streamed text tokens
	OnDeltaReset func()          // optional callback when tool calls follow text
	Overrides    *RunOverrides   // optional explicit model/thinking overrides
}

// runSlot holds the state of a single active run.
// Using a pointer allows safe identity comparison when cleaning up.
type runSlot struct {
	cancel context.CancelFunc
}

// RuntimeHub manages concurrent agent runs keyed by canonical session.
// At most one run per session key is active at any time; a new Submit
// automatically cancels the previous run for the same session.
//
// The hub owns agent creation through its RunResolver, making it the
// single owner of run lifecycle, tool execution, and session mutation.
type RuntimeHub struct {
	mu       sync.Mutex
	active   map[SessionKey]*runSlot
	resolver *RunResolver
}

// NewRuntimeHub creates a new RuntimeHub with the given resolver.
// The resolver is used to build tool-calling agents for each run.
func NewRuntimeHub(resolver *RunResolver) *RuntimeHub {
	return &RuntimeHub{
		active:   make(map[SessionKey]*runSlot),
		resolver: resolver,
	}
}

// Submit starts an agent run asynchronously for the given request.
// The hub resolves the agent profile, AI client, and tool registry
// internally via the RunResolver. If another run is already active
// for the same session key it is cancelled first.
// Returns a channel that receives exactly one RunEvent then closes.
func (h *RuntimeHub) Submit(req RunRequest) <-chan RunEvent {
	events := make(chan RunEvent, 1)

	// Resolve agent components.
	components, err := h.resolver.Resolve(req.ChatID, req.Overrides)
	if err != nil {
		events <- RunEvent{Type: RunEventError, Err: err}
		close(events)
		return events
	}

	// Wire callbacks.
	if req.OnToolEvent != nil {
		components.Agent.SetToolEventCallback(req.OnToolEvent)
	}
	if req.OnDelta != nil {
		components.Agent.SetDeltaCallback(req.OnDelta)
	}
	if req.OnDeltaReset != nil {
		components.Agent.SetDeltaResetCallback(req.OnDeltaReset)
	}

	profileName := components.Profile.Name

	ctx, cancel := context.WithCancel(req.Context)
	slot := &runSlot{cancel: cancel}

	h.mu.Lock()
	if existing, ok := h.active[req.SessionKey]; ok {
		log.Printf("[hub] cancelling existing run for session %s", req.SessionKey)
		existing.cancel()
	}
	h.active[req.SessionKey] = slot
	h.mu.Unlock()

	go func() {
		defer func() {
			h.mu.Lock()
			// Only remove our slot; a newer Submit may have replaced it already.
			if h.active[req.SessionKey] == slot {
				delete(h.active, req.SessionKey)
			}
			h.mu.Unlock()
			cancel()
			close(events)
		}()

		log.Printf("[hub] starting run for session %s (agent: %s)", req.SessionKey, profileName)
		result, err := components.Agent.ProcessRequestWithContent(ctx, req.Content, req.UserContent, req.Session, req.History)
		if err != nil {
			if ctx.Err() != nil {
				log.Printf("[hub] run for session %s was cancelled", req.SessionKey)
			} else {
				log.Printf("[hub] run for session %s failed: %v", req.SessionKey, err)
			}
			events <- RunEvent{Type: RunEventError, Err: err, ProfileName: profileName}
			return
		}

		log.Printf("[hub] run for session %s done", req.SessionKey)
		events <- RunEvent{Type: RunEventDone, Result: result, ProfileName: profileName}
	}()

	return events
}

// Cancel stops the active run for the given session key (no-op if none).
func (h *RuntimeHub) Cancel(key SessionKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if slot, ok := h.active[key]; ok {
		log.Printf("[hub] cancelling run for session %s", key)
		slot.cancel()
	}
}

// IsActive reports whether a run is currently active for the given session key.
func (h *RuntimeHub) IsActive(key SessionKey) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.active[key]
	return ok
}
