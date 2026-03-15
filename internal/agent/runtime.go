// Legacy hub/subagent runtime compatibility layer.
// New architecture work must target internal/runtime and the chat/jobs
// contract in docs/ARCHITECTURE.md. Keep changes here limited to
// compatibility fixes and removal prep.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/delegation"
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
	Job          *delegation.Job // optional delegated-run contract
	IsSubagent   bool            // true = don't inject browser_task into the run
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

	if req.Context == nil {
		req.Context = context.Background()
	}

	var job *delegation.Job
	if req.Job != nil {
		normalized := req.Job.WithDefaults()
		job = &normalized
	}

	overrides := req.Overrides
	if job != nil && (job.Model != "" || job.Thinking != "") {
		merged := RunOverrides{}
		if overrides != nil {
			merged = *overrides
		}
		if merged.Model == "" {
			merged.Model = job.Model
		}
		if merged.ThinkLevel == "" {
			merged.ThinkLevel = job.Thinking
		}
		overrides = &merged
	}

	// Resolve agent components.
	components, err := h.resolver.Resolve(req.ChatID, overrides, job, req.IsSubagent)
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
	if job != nil {
		components.Agent.SetMaxToolCalls(job.MaxToolCalls)
	}

	// Promote timed-out tool calls into isolated subagent runs for main sessions
	// so the active conversation stays responsive. Subagents themselves never
	// auto-spawn further subagents.
	if req.IsSubagent {
		components.Agent.SetToolTimeoutCallback(0, nil)
	} else {
		components.Agent.SetToolTimeoutCallback(DefaultToolTimeout, func(toolName, argsJSON string) string {
			subKey := SessionKey(fmt.Sprintf("subagent:%d:%d", req.ChatID, time.Now().UnixNano()))
			task := fmt.Sprintf("Execute tool '%s' with arguments: %s", toolName, argsJSON)
			log.Printf("[hub] tool %s timed out for session %s — spawning subagent %s", toolName, req.SessionKey, subKey)

			h.Submit(RunRequest{
				SessionKey: subKey,
				ChatID:     req.ChatID,
				Content:    task,
				Context:    context.Background(),
				IsSubagent: true,
			})

			return fmt.Sprintf("⏳ Tool '%s' exceeded %s — moved to subagent. You'll get a notification when it finishes.", toolName, DefaultToolTimeout)
		})
	}

	profileName := components.Profile.Name
	content := req.Content
	if job != nil {
		content = job.ContractPrompt(req.Content)
	}

	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if job != nil && job.MaxDuration > 0 {
		ctx, cancel = context.WithTimeout(req.Context, job.MaxDuration)
	} else {
		ctx, cancel = context.WithCancel(req.Context)
	}
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
		result, err := components.Agent.ProcessRequestWithContent(ctx, content, req.UserContent, req.Session, req.History)
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

// SubmitAndWait spawns a subagent run and blocks until it completes or times out.
// Implements the SubagentSubmitter interface used by browser_task tool.
func (h *RuntimeHub) SubmitAndWait(ctx context.Context, chatID int64, task string, job delegation.Job) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	job = job.WithDefaults()

	subKey := SessionKey(fmt.Sprintf("subagent:%d:%d", chatID, time.Now().UnixNano()))

	events := h.Submit(RunRequest{
		SessionKey: subKey,
		ChatID:     chatID,
		Content:    task,
		Context:    ctx,
		Job:        &job,
		IsSubagent: true,
	})

	select {
	case ev := <-events:
		switch ev.Type {
		case RunEventDone:
			if ev.Result != nil {
				return ev.Result.Message, nil
			}
			return "", fmt.Errorf("subagent returned nil result")
		case RunEventError:
			if errors.Is(ev.Err, context.DeadlineExceeded) {
				return "", fmt.Errorf("subagent timed out after %s", job.MaxDuration)
			}
			return "", fmt.Errorf("subagent error: %w", ev.Err)
		default:
			return "", fmt.Errorf("unexpected event type: %s", ev.Type)
		}
	case <-ctx.Done():
		h.Cancel(subKey)
		return "", ctx.Err()
	}
}
