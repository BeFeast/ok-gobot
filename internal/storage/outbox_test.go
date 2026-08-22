package storage

import (
	"path/filepath"
	"testing"
)

// The bug these tests pin down: a finished background result lived only in a
// local variable. If the send failed, or the process died between "work done"
// and "message sent", the answer was gone for good — four times in the live
// journal (2026-08-13, 08-14 twice, 08-21). The fix is to commit the text
// before attempting any send, so a restart can still deliver it.

func TestOutboxSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "outbox.db")

	store, err := New(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	id, err := store.EnqueueOutbox(4242, "the answer nobody saw", "run-abc")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if id == 0 {
		t.Fatal("expected a row id")
	}
	// Simulate the crash: the process dies before anything is sent.
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close() //nolint:errcheck

	pending, err := reopened.PendingOutbox(10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 undelivered message after restart, got %d", len(pending))
	}
	if pending[0].Text != "the answer nobody saw" || pending[0].ChatID != 4242 {
		t.Fatalf("payload not preserved across restart: %+v", pending[0])
	}
}

func TestOutboxDeliveredExactlyOnce(t *testing.T) {
	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	id, err := store.EnqueueOutbox(7, "done", "run-once")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := store.MarkOutboxDelivered(id, 555); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	// Marking twice must be a no-op, not a second delivery.
	if err := store.MarkOutboxDelivered(id, 555); err != nil {
		t.Fatalf("second mark should be harmless: %v", err)
	}

	pending, err := store.PendingOutbox(10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("delivered message must not be re-sent, got %d pending", len(pending))
	}
}

func TestOutboxGivesUpButStaysVisible(t *testing.T) {
	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	id, err := store.EnqueueOutbox(9, "unsendable", "run-fail")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	for i := 0; i < outboxMaxAttempts; i++ {
		if err := store.RecordOutboxFailure(id, "telegram said no"); err != nil {
			t.Fatalf("record failure %d: %v", i, err)
		}
	}

	pending, err := store.PendingOutbox(10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("exhausted message must stop being retried, got %d pending", len(pending))
	}

	failed, err := store.FailedOutbox(10)
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("exhausted message must stay visible, got %d", len(failed))
	}
	if failed[0].LastError != "telegram said no" {
		t.Fatalf("last error not kept: %q", failed[0].LastError)
	}
}
