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

func TestTesseraFreshClaimRecoveredAfterLaterEligibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	s, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.EnqueueTesseraDelivery(TesseraDelivery{EventKey: "event", Fingerprint: "policy", ChatID: 123, SenderID: 123, Target: "target", Metadata: "meta", Text: "attention"})
	if err != nil {
		t.Fatal(err)
	}
	if won, err := s.ClaimOutbox(id); err != nil || !won {
		t.Fatal("claim failed")
	}
	s.Close()
	s, err = New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n, err := s.ReclaimStaleOutbox(5); err != nil || n != 0 {
		t.Fatal("fresh claim unexpectedly reclaimed")
	}
	if _, err = s.db.Exec(`UPDATE outbox SET updated_at=datetime('now','-6 minutes') WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReclaimStaleTesseraOutbox(); err != nil || n != 1 {
		t.Fatal("later periodic recovery failed")
	}
	pending, err := s.PendingOutbox(20)
	if err != nil || len(pending) != 1 || pending[0].ID != id {
		t.Fatal("original row not recovered")
	}
}
func TestTesseraFailedDeliveryExplicitRetryKeepsAuthorityAndAttempts(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "bot.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.EnqueueTesseraDelivery(TesseraDelivery{EventKey: "event", Fingerprint: "policy", ChatID: 123, SenderID: 123, Target: "target", Metadata: "meta", Text: "attention"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err = s.RecordOutboxFailure(id, "lost ack"); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []struct {
		fingerprint         string
		chat, topic, sender int64
	}{{"wrong", 123, 0, 123}, {"policy", 123, 7, 123}, {"policy", 123, 0, 999}} {
		if n, err := s.RetryTesseraOutbox(v.fingerprint, v.chat, v.topic, v.sender); err != nil || n != 0 {
			t.Fatal("wrong authority requeued")
		}
	}
	if n, err := s.RetryTesseraOutbox("policy", 123, 0, 123); err != nil || n != 1 {
		t.Fatal("explicit retry failed")
	}
	pending, err := s.PendingOutbox(20)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 5 {
		t.Fatal("attempt history lost")
	}
	if err = s.MarkOutboxDelivered(id, 88); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TesseraReplyBinding("policy", 123, 0, 123, 88); err != nil {
		t.Fatal("retry binding failed")
	}
}
