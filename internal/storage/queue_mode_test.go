package storage

import "testing"

func TestSaveSessionDefaultsQueueModeToInterrupt(t *testing.T) {
	s := newV2TestStore(t)

	if err := s.SaveSession(42, "seed"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	mode, err := s.GetSessionOption(42, "queue_mode")
	if err != nil {
		t.Fatalf("GetSessionOption: %v", err)
	}
	if mode != "interrupt" {
		t.Fatalf("queue_mode = %q, want interrupt", mode)
	}
}
