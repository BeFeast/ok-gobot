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

// The race an automated review caught on PR #18: the inline completion path and
// the background retry loop could both pick up the same pending row and send it
// twice. Only one claim may win.
func TestOutboxClaimIsExclusive(t *testing.T) {
	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	id, err := store.EnqueueOutbox(7, "contested", "task")
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	first, err := store.ClaimOutbox(id)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	second, err := store.ClaimOutbox(id)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if !first || second {
		t.Fatalf("exactly one claim must win, got first=%v second=%v", first, second)
	}

	// A claimed row is invisible to the retry loop while its owner is sending.
	pending, err := store.PendingOutbox(10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("claimed row must not be offered again, got %d", len(pending))
	}
}

// A row committed by a caller that is about to send it itself must not be
// visible to the retry loop at all — that is what closes the race at the source.
func TestOutboxEnqueueSendingIsNotOffered(t *testing.T) {
	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	if _, err := store.EnqueueOutboxSending(7, "mine to send", "task"); err != nil {
		t.Fatalf("enqueue sending: %v", err)
	}
	pending, err := store.PendingOutbox(10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("a row being sent by its author must not be offered, got %d", len(pending))
	}
}

// If the process dies mid-send the row stays claimed by a pid that no longer
// exists. It must come back, or the answer is lost in a new way.
func TestOutboxReclaimsAbandonedSend(t *testing.T) {
	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	id, err := store.EnqueueOutboxSending(7, "abandoned", "task")
	if err != nil {
		t.Fatalf("enqueue sending: %v", err)
	}
	// Age the claim past the staleness cutoff.
	if _, err := store.DB().Exec(
		`UPDATE outbox SET updated_at = datetime('now', '-30 minutes') WHERE id = ?`, id,
	); err != nil {
		t.Fatalf("age row: %v", err)
	}

	n, err := store.ReclaimStaleOutbox(5)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 reclaimed row, got %d", n)
	}
	pending, err := store.PendingOutbox(10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("reclaimed row must be offered again, got %d", len(pending))
	}
}

// A failed attempt must release the claim, or a single failure would park the
// answer forever.
func TestOutboxFailureReleasesClaim(t *testing.T) {
	store := newTestStore(t)
	defer store.Close() //nolint:errcheck

	id, err := store.EnqueueOutboxSending(7, "will fail once", "task")
	if err != nil {
		t.Fatalf("enqueue sending: %v", err)
	}
	if err := store.RecordOutboxFailure(id, "telegram hiccup"); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	pending, err := store.PendingOutbox(10)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("after a failure the row must be retryable, got %d pending", len(pending))
	}
	if pending[0].Attempts != 1 {
		t.Fatalf("attempt not counted: %d", pending[0].Attempts)
	}
}
