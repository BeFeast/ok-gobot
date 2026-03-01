package bot

import (
	"sync"
	"testing"

	"gopkg.in/telebot.v4"
)

func TestAckHandleManager_SetAndExists(t *testing.T) {
	m := NewAckHandleManager()

	if m.Exists(42) {
		t.Fatal("expected no handle for chatID=42")
	}

	m.Set(42, &AckHandle{ChatID: 42, Message: &telebot.Message{ID: 100}})

	if !m.Exists(42) {
		t.Fatal("expected handle to exist for chatID=42 after Set")
	}
}

func TestAckHandleManager_TakeRemoves(t *testing.T) {
	m := NewAckHandleManager()
	m.Set(1, &AckHandle{ChatID: 1, Message: &telebot.Message{ID: 10}})

	got := m.Take(1)
	if got == nil {
		t.Fatal("expected non-nil AckHandle from Take")
	}
	if got.ChatID != 1 || got.Message.ID != 10 {
		t.Errorf("unexpected handle content: %+v", got)
	}

	// Second Take must return nil — consumed
	if m.Take(1) != nil {
		t.Fatal("expected nil on second Take — handle should be gone")
	}
	if m.Exists(1) {
		t.Fatal("expected Exists to return false after Take")
	}
}

func TestAckHandleManager_TakeNonExistent(t *testing.T) {
	m := NewAckHandleManager()
	if got := m.Take(999); got != nil {
		t.Fatalf("expected nil for unknown chatID, got %+v", got)
	}
}

func TestAckHandleManager_SetOverwrites(t *testing.T) {
	m := NewAckHandleManager()
	m.Set(5, &AckHandle{ChatID: 5, Message: &telebot.Message{ID: 1}})
	m.Set(5, &AckHandle{ChatID: 5, Message: &telebot.Message{ID: 2}})

	got := m.Take(5)
	if got == nil || got.Message.ID != 2 {
		t.Fatalf("expected second handle (msg_id=2) to overwrite first, got %+v", got)
	}
}

func TestAckHandleManager_ConcurrentAccess(t *testing.T) {
	m := NewAckHandleManager()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			m.Set(id, &AckHandle{ChatID: id, Message: &telebot.Message{ID: int(id)}})
			m.Exists(id)
			m.Take(id)
		}(int64(i))
	}
	wg.Wait()
	// Should not panic or race
}
