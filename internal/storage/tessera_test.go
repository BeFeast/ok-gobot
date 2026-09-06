package storage

import (
	"path/filepath"
	"testing"
)

func TestTesseraOutboxInvisibleToOldBinary(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.EnqueueTesseraDelivery(TesseraDelivery{EventKey: "event", Fingerprint: "policy", ChatID: 123, SenderID: 123, Target: `{"revision":"old"}`, Metadata: `{"kind":"tessera-attention"}`, Text: "attention"})
	if err != nil {
		t.Fatal(err)
	}
	assertOldCannotDrain := func() {
		t.Helper()
		var count int
		// These are the old released binary's exact lifecycle state filters.
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE state IN ('pending','sending','failed')`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatal("old binary can drain Tessera row")
		}
	}
	assertOldCannotDrain()
	pending, err := s.PendingOutbox(20)
	if err != nil || len(pending) != 1 {
		t.Fatal("new binary cannot see pending")
	}
	won, err := s.ClaimOutbox(id)
	if err != nil || !won {
		t.Fatal("claim failed")
	}
	assertOldCannotDrain()
	if _, err = s.db.Exec(`UPDATE outbox SET updated_at=datetime('now','-10 minutes') WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReclaimStaleOutbox(5); err != nil || n != 1 {
		t.Fatal("reclaim failed")
	}
	assertOldCannotDrain()
	for i := 0; i < 5; i++ {
		if err = s.RecordOutboxFailure(id, "synthetic"); err != nil {
			t.Fatal(err)
		}
		assertOldCannotDrain()
	}
	failed, err := s.FailedOutbox(20)
	if err != nil || len(failed) != 1 {
		t.Fatal("failed row lost")
	}
}
