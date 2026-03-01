// Package runtime implements the per-session worker mailbox model.
// It is the sole owner of agent execution scheduling.
//
// Each unique session key gets one SessionWorker goroutine that processes
// requests one at a time from a buffered queue. Different session keys run
// fully concurrently. On every inbound request, the caller receives
// immediate acknowledgment via AckHandle (idle → active, busy → queued),
// and all state transitions are broadcast as RuntimeEvents to subscribers.
package runtime

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// EventType describes the kind of a RuntimeEvent.
type EventType string

const (
	// EventQueued is emitted when a request is accepted but the session is busy.
	EventQueued EventType = "queued"
	// EventActive is emitted when a request starts executing.
	EventActive EventType = "active"
	// EventUpdate is emitted by the RunFunc during execution for progress updates.
	EventUpdate EventType = "update"
	// EventDone is emitted when a request completes successfully.
	EventDone EventType = "done"
	// EventError is emitted when a request completes with an error.
	EventError EventType = "error"
)

// RuntimeEvent is broadcast to all Hub subscribers on every state change.
type RuntimeEvent struct {
	Type       EventType
	SessionKey string
	RequestID  string
	Payload    any
	Err        error
	Timestamp  time.Time
}

// AckHandle is returned by Hub.Submit. The Hub and the RunFunc call its
// methods to signal run state transitions to the caller.
type AckHandle interface {
	// AcceptQueued signals that the session was busy; request is enqueued.
	AcceptQueued()
	// AcceptActive signals that the request is now executing.
	AcceptActive()
	// Update sends an in-flight progress payload.
	Update(payload any)
	// Close marks the request done. nil err means success.
	Close(err error)
}

// RunFunc is the work to be executed for a session request.
// It receives a cancellable context and an AckHandle for signalling progress.
// Implementations must call ack.Close when done (typically deferred).
type RunFunc func(ctx context.Context, ack AckHandle)

// Hub manages one SessionWorker per session key.
type Hub struct {
	mu         sync.Mutex
	workers    map[string]*SessionWorker
	subsMu     sync.Mutex
	subs       []chan<- RuntimeEvent
	ctx        context.Context
	queueDepth int
}

// NewHub creates a Hub. queueDepth controls the per-session inbound buffer size.
func NewHub(ctx context.Context, queueDepth int) *Hub {
	if queueDepth <= 0 {
		queueDepth = 64
	}
	return &Hub{
		workers:    make(map[string]*SessionWorker),
		ctx:        ctx,
		queueDepth: queueDepth,
	}
}

// Subscribe adds ch to receive all RuntimeEvents. ch must be buffered to
// avoid blocking the hub; events are dropped (with a log warning) if full.
func (h *Hub) Subscribe(ch chan<- RuntimeEvent) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	h.subs = append(h.subs, ch)
}

// Unsubscribe removes ch from the subscriber list.
func (h *Hub) Unsubscribe(ch chan<- RuntimeEvent) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	out := h.subs[:0]
	for _, s := range h.subs {
		if s != ch {
			out = append(out, s)
		}
	}
	h.subs = out
}

// emit broadcasts ev to all subscribers, dropping events to full channels.
func (h *Hub) emit(ev RuntimeEvent) {
	h.subsMu.Lock()
	subs := make([]chan<- RuntimeEvent, len(h.subs))
	copy(subs, h.subs)
	h.subsMu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
			log.Printf("[runtime] subscriber full, dropping event %s for session %s", ev.Type, ev.SessionKey)
		}
	}
}

// Submit enqueues run under sessionKey. It returns an AckHandle immediately;
// AcceptQueued or AcceptActive is called synchronously before Submit returns.
func (h *Hub) Submit(sessionKey, requestID string, run RunFunc) AckHandle {
	h.mu.Lock()
	w, ok := h.workers[sessionKey]
	if !ok {
		w = newSessionWorker(h.ctx, sessionKey, h.queueDepth, h)
		h.workers[sessionKey] = w
	}
	h.mu.Unlock()

	ack := &handle{
		sessionKey: sessionKey,
		requestID:  requestID,
		hub:        h,
	}

	req := runRequest{id: requestID, run: run, ack: ack}

	// Signal immediately: queued if the worker has an active run, active otherwise.
	if w.isRunning() {
		ack.wasQueued.Store(true)
		ack.AcceptQueued()
	} else {
		ack.AcceptActive()
	}

	w.queue <- req
	return ack
}

// CancelSession cancels the currently executing request for sessionKey.
// Has no effect if the session is idle.
func (h *Hub) CancelSession(sessionKey string) {
	h.mu.Lock()
	w, ok := h.workers[sessionKey]
	h.mu.Unlock()
	if ok {
		w.cancelCurrent()
	}
}

// ── runRequest ───────────────────────────────────────────────────────────────

type runRequest struct {
	id  string
	run RunFunc
	ack *handle
}

// ── SessionWorker ────────────────────────────────────────────────────────────

// SessionWorker processes one request at a time from a buffered queue.
// A single goroutine runs the loop; different SessionWorkers run concurrently.
type SessionWorker struct {
	sessionKey string
	queue      chan runRequest
	hub        *Hub
	running    atomic.Bool

	cancelMu sync.Mutex
	cancelFn context.CancelFunc
}

func newSessionWorker(ctx context.Context, key string, depth int, hub *Hub) *SessionWorker {
	sw := &SessionWorker{
		sessionKey: key,
		queue:      make(chan runRequest, depth),
		hub:        hub,
	}
	go sw.loop(ctx)
	return sw
}

// isRunning reports whether a run is currently active in this worker.
func (sw *SessionWorker) isRunning() bool {
	return sw.running.Load()
}

// cancelCurrent cancels the active run context, if any.
func (sw *SessionWorker) cancelCurrent() {
	sw.cancelMu.Lock()
	defer sw.cancelMu.Unlock()
	if sw.cancelFn != nil {
		sw.cancelFn()
	}
}

func (sw *SessionWorker) loop(ctx context.Context) {
	log.Printf("[runtime] session worker started: %s", sw.sessionKey)
	for {
		select {
		case <-ctx.Done():
			log.Printf("[runtime] session worker stopped: %s", sw.sessionKey)
			return
		case req := <-sw.queue:
			sw.execute(ctx, req)
		}
	}
}

func (sw *SessionWorker) execute(ctx context.Context, req runRequest) {
	runCtx, cancel := context.WithCancel(ctx)
	sw.cancelMu.Lock()
	sw.cancelFn = cancel
	sw.cancelMu.Unlock()

	sw.running.Store(true)

	// If the request was initially queued, signal it is now active.
	if req.ack.wasQueued.Load() {
		req.ack.AcceptActive()
	}

	log.Printf("[runtime] session %s: executing request %s", sw.sessionKey, req.id)

	defer func() {
		cancel()
		sw.cancelMu.Lock()
		sw.cancelFn = nil
		sw.cancelMu.Unlock()
		sw.running.Store(false)
		log.Printf("[runtime] session %s: request %s completed", sw.sessionKey, req.id)
	}()

	req.run(runCtx, req.ack)
}

// ── handle ───────────────────────────────────────────────────────────────────

// handle is the concrete AckHandle returned by Hub.Submit.
type handle struct {
	sessionKey string
	requestID  string
	hub        *Hub
	wasQueued  atomic.Bool
	closeOnce  sync.Once
}

func (h *handle) AcceptQueued() {
	h.hub.emit(RuntimeEvent{
		Type:       EventQueued,
		SessionKey: h.sessionKey,
		RequestID:  h.requestID,
		Timestamp:  time.Now(),
	})
}

func (h *handle) AcceptActive() {
	h.hub.emit(RuntimeEvent{
		Type:       EventActive,
		SessionKey: h.sessionKey,
		RequestID:  h.requestID,
		Timestamp:  time.Now(),
	})
}

func (h *handle) Update(payload any) {
	h.hub.emit(RuntimeEvent{
		Type:       EventUpdate,
		SessionKey: h.sessionKey,
		RequestID:  h.requestID,
		Payload:    payload,
		Timestamp:  time.Now(),
	})
}

func (h *handle) Close(err error) {
	h.closeOnce.Do(func() {
		evType := EventDone
		if err != nil {
			evType = EventError
		}
		h.hub.emit(RuntimeEvent{
			Type:       evType,
			SessionKey: h.sessionKey,
			RequestID:  h.requestID,
			Err:        err,
			Timestamp:  time.Now(),
		})
	})
}
