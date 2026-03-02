package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestRuntimeHubIsActiveAndCancel verifies IsActive and Cancel semantics.
func TestRuntimeHubIsActiveAndCancel(t *testing.T) {
	hub := NewRuntimeHub()
	key := NewDMSessionKey(100)

	// Before any run, IsActive must be false.
	if hub.IsActive(key) {
		t.Fatal("expected IsActive=false before any submit")
	}

	// Cancel on an idle session must not panic.
	hub.Cancel(key)

	// Inject a slot directly to simulate an active run without needing a real agent.
	ctx, cancel := context.WithCancel(context.Background())
	hub.mu.Lock()
	hub.active[key] = &runSlot{cancel: cancel}
	hub.mu.Unlock()

	if !hub.IsActive(key) {
		t.Fatal("expected IsActive=true after slot injection")
	}

	hub.Cancel(key)

	// The context must be cancelled within a short window.
	select {
	case <-ctx.Done():
		// ok
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Cancel did not cancel the slot context within 100ms")
	}

	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("expected context.Canceled after Cancel, got %v", ctx.Err())
	}

	// Simulate the goroutine cleanup removing the slot.
	hub.mu.Lock()
	delete(hub.active, key)
	hub.mu.Unlock()

	if hub.IsActive(key) {
		t.Fatal("expected IsActive=false after slot removal")
	}
}

// TestRuntimeHubCancelIdleIsNoop verifies Cancel on an idle session is a no-op.
func TestRuntimeHubCancelIdleIsNoop(t *testing.T) {
	hub := NewRuntimeHub()
	key := NewGroupSessionKey(999)

	// Must not panic on repeated calls.
	hub.Cancel(key)
	hub.Cancel(key)

	if hub.IsActive(key) {
		t.Fatal("expected IsActive=false for idle session")
	}
}

// TestRuntimeHubSessionKeyFormats verifies the canonical session key helpers.
func TestRuntimeHubSessionKeyFormats(t *testing.T) {
	dm := NewDMSessionKey(12345)
	group := NewGroupSessionKey(67890)

	if dm != "dm:12345" {
		t.Errorf("DM key = %q, want %q", dm, "dm:12345")
	}
	if group != "group:67890" {
		t.Errorf("Group key = %q, want %q", group, "group:67890")
	}
}

// TestRuntimeHubCancelErrorIsContextCanceled verifies that when a running slot
// is cancelled, the context's error is context.Canceled — the same error that
// would propagate through ProcessRequest and be sent as RunEventError.
// This is relevant for the /abort command which calls hub.Cancel and expects
// the bot to detect the error as a cancellation (not a real failure).
func TestRuntimeHubCancelErrorIsContextCanceled(t *testing.T) {
	hub := NewRuntimeHub()
	key := NewDMSessionKey(200)

	ctx, cancel := context.WithCancel(context.Background())
	hub.mu.Lock()
	hub.active[key] = &runSlot{cancel: cancel}
	hub.mu.Unlock()

	hub.Cancel(key)

	<-ctx.Done()

	// errors.Is must match context.Canceled so that the bot's hub_handler
	// can distinguish a user-initiated abort from a real error.
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Errorf("errors.Is(ctx.Err(), context.Canceled) = false, got %v", ctx.Err())
	}
}
