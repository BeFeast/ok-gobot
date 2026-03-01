package runtime

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// collectEvents drains events from ch until it is closed or the deadline passes.
func collectEvents(ch <-chan RuntimeEvent, timeout time.Duration) []RuntimeEvent {
	var events []RuntimeEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-deadline:
			return events
		}
	}
}

// TestSessionWorkerOneRunAtATime verifies that a single session key never
// executes more than one run concurrently.
func TestSessionWorkerOneRunAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := NewHub(ctx, 10)

	var mu sync.Mutex
	var concurrent, maxConcurrent int
	var wg sync.WaitGroup

	const numRequests = 5
	wg.Add(numRequests)

	for i := 0; i < numRequests; i++ {
		id := fmt.Sprintf("req-%d", i)
		hub.Submit("session-A", id, func(ctx context.Context, ack AckHandle) {
			defer wg.Done()
			defer ack.Close(nil)

			mu.Lock()
			concurrent++
			if concurrent > maxConcurrent {
				maxConcurrent = concurrent
			}
			mu.Unlock()

			time.Sleep(20 * time.Millisecond)

			mu.Lock()
			concurrent--
			mu.Unlock()
		})
	}

	wg.Wait()

	if maxConcurrent > 1 {
		t.Errorf("same session: expected max 1 concurrent run, got %d", maxConcurrent)
	}
}

// TestDifferentSessionsConcurrent verifies that two different session keys
// execute their runs concurrently without blocking each other.
func TestDifferentSessionsConcurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := NewHub(ctx, 10)

	var mu sync.Mutex
	var concurrent, maxConcurrent int
	var wg sync.WaitGroup

	wg.Add(2)

	for i := 0; i < 2; i++ {
		key := fmt.Sprintf("session-%d", i)
		hub.Submit(key, "req-1", func(ctx context.Context, ack AckHandle) {
			defer wg.Done()
			defer ack.Close(nil)

			mu.Lock()
			concurrent++
			if concurrent > maxConcurrent {
				maxConcurrent = concurrent
			}
			mu.Unlock()

			time.Sleep(100 * time.Millisecond)

			mu.Lock()
			concurrent--
			mu.Unlock()
		})
	}

	wg.Wait()

	if maxConcurrent < 2 {
		t.Errorf("different sessions: expected both to run concurrently, max concurrent was %d", maxConcurrent)
	}
}

// TestAckHandleEvents verifies that the correct RuntimeEvents are emitted
// in the expected order for a simple run.
func TestAckHandleEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := NewHub(ctx, 10)
	subCh := make(chan RuntimeEvent, 50)
	hub.Subscribe(subCh)

	var wg sync.WaitGroup
	wg.Add(1)

	hub.Submit("session-X", "req-1", func(ctx context.Context, ack AckHandle) {
		defer wg.Done()
		ack.Update("progress")
		ack.Close(nil)
	})

	wg.Wait()
	// Give the hub a moment to deliver all events.
	time.Sleep(10 * time.Millisecond)

	events := collectEvents(subCh, 100*time.Millisecond)

	want := []EventType{EventActive, EventUpdate, EventDone}
	if len(events) < len(want) {
		t.Fatalf("expected at least %d events, got %d: %v", len(want), len(events), events)
	}

	for i, w := range want {
		if events[i].Type != w {
			t.Errorf("event[%d]: want %s, got %s", i, w, events[i].Type)
		}
	}
}

// TestQueuedThenActive verifies that when a session is busy, additional
// requests get EventQueued followed by EventActive when they start.
func TestQueuedThenActive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := NewHub(ctx, 10)
	subCh := make(chan RuntimeEvent, 100)
	hub.Subscribe(subCh)

	startFirst := make(chan struct{})
	releaseFirst := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)

	// First request: holds the worker until released.
	hub.Submit("session-Q", "req-1", func(ctx context.Context, ack AckHandle) {
		defer wg.Done()
		close(startFirst)
		<-releaseFirst
		ack.Close(nil)
	})

	// Wait until the first request is running so the second will be queued.
	<-startFirst

	hub.Submit("session-Q", "req-2", func(ctx context.Context, ack AckHandle) {
		defer wg.Done()
		ack.Close(nil)
	})

	// Release the first request.
	close(releaseFirst)
	wg.Wait()
	time.Sleep(10 * time.Millisecond)

	events := collectEvents(subCh, 100*time.Millisecond)

	// Count event types.
	counts := make(map[EventType]int)
	for _, ev := range events {
		counts[ev.Type]++
	}

	if counts[EventQueued] < 1 {
		t.Errorf("expected at least 1 EventQueued, got %d; events: %v", counts[EventQueued], events)
	}
	if counts[EventActive] < 2 {
		t.Errorf("expected at least 2 EventActive (one per request), got %d; events: %v", counts[EventActive], events)
	}
}

// TestCancelSession verifies that CancelSession interrupts the active run.
func TestCancelSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := NewHub(ctx, 10)

	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	hub.Submit("session-C", "req-1", func(runCtx context.Context, ack AckHandle) {
		defer wg.Done()
		close(started)
		<-runCtx.Done() // block until cancelled
		ack.Close(runCtx.Err())
	})

	<-started
	hub.CancelSession("session-C")

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		// ok
	case <-time.After(2 * time.Second):
		t.Error("CancelSession did not cancel the active run in time")
	}
}

// TestAckHandleErrorClose verifies that Close with a non-nil error emits EventError.
func TestAckHandleErrorClose(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hub := NewHub(ctx, 10)
	subCh := make(chan RuntimeEvent, 20)
	hub.Subscribe(subCh)

	var wg sync.WaitGroup
	wg.Add(1)

	hub.Submit("session-E", "req-err", func(ctx context.Context, ack AckHandle) {
		defer wg.Done()
		ack.Close(fmt.Errorf("something went wrong"))
	})

	wg.Wait()
	time.Sleep(10 * time.Millisecond)

	events := collectEvents(subCh, 100*time.Millisecond)

	var gotError bool
	for _, ev := range events {
		if ev.Type == EventError {
			gotError = true
			if ev.Err == nil {
				t.Error("EventError has nil Err field")
			}
		}
	}
	if !gotError {
		t.Errorf("expected EventError, got: %v", events)
	}
}
