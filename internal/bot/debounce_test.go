package bot

import (
	"sync"
	"testing"
	"time"
)

func TestDebouncer_SingleMessage(t *testing.T) {
	debouncer := NewDebouncer(100 * time.Millisecond)
	defer debouncer.Stop()

	var wg sync.WaitGroup
	wg.Add(1)

	var result string
	callback := func(combined string) {
		result = combined
		wg.Done()
	}

	debouncer.Debounce(123, "hello", callback)

	wg.Wait()

	if result != "hello" {
		t.Errorf("Expected 'hello', got '%s'", result)
	}
}

func TestDebouncer_MultipleMessages(t *testing.T) {
	debouncer := NewDebouncer(200 * time.Millisecond)
	defer debouncer.Stop()

	var wg sync.WaitGroup
	wg.Add(1)

	var result string
	callback := func(combined string) {
		result = combined
		wg.Done()
	}

	chatID := int64(123)
	debouncer.Debounce(chatID, "hello", callback)
	time.Sleep(50 * time.Millisecond)
	debouncer.Debounce(chatID, "world", callback)
	time.Sleep(50 * time.Millisecond)
	debouncer.Debounce(chatID, "test", callback)

	wg.Wait()

	expected := "hello\nworld\ntest"
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

func TestDebouncer_MultipleChatsSeparate(t *testing.T) {
	debouncer := NewDebouncer(100 * time.Millisecond)
	defer debouncer.Stop()

	var wg sync.WaitGroup
	wg.Add(2)

	var result1, result2 string
	callback1 := func(combined string) {
		result1 = combined
		wg.Done()
	}
	callback2 := func(combined string) {
		result2 = combined
		wg.Done()
	}

	debouncer.Debounce(123, "chat1", callback1)
	debouncer.Debounce(456, "chat2", callback2)

	wg.Wait()

	if result1 != "chat1" {
		t.Errorf("Chat 1: Expected 'chat1', got '%s'", result1)
	}
	if result2 != "chat2" {
		t.Errorf("Chat 2: Expected 'chat2', got '%s'", result2)
	}
}

func TestDebouncer_Stop(t *testing.T) {
	debouncer := NewDebouncer(100 * time.Millisecond)

	called := false
	callback := func(combined string) {
		called = true
	}

	debouncer.Debounce(123, "test", callback)
	debouncer.Stop()

	time.Sleep(150 * time.Millisecond)

	if called {
		t.Error("Callback should not be called after Stop()")
	}
}

func TestDebouncer_DefaultWindow(t *testing.T) {
	debouncer := NewDebouncer(0) // Should use default
	defer debouncer.Stop()

	if debouncer.window != 1500*time.Millisecond {
		t.Errorf("Expected default window of 1500ms, got %v", debouncer.window)
	}
}

func TestDebouncer_GetPendingCount(t *testing.T) {
	debouncer := NewDebouncer(200 * time.Millisecond)
	defer debouncer.Stop()

	if debouncer.GetPendingCount() != 0 {
		t.Error("Expected 0 pending messages initially")
	}

	callback := func(combined string) {}

	debouncer.Debounce(123, "msg1", callback)
	if debouncer.GetPendingCount() != 1 {
		t.Errorf("Expected 1 pending chat, got %d", debouncer.GetPendingCount())
	}

	debouncer.Debounce(456, "msg2", callback)
	if debouncer.GetPendingCount() != 2 {
		t.Errorf("Expected 2 pending chats, got %d", debouncer.GetPendingCount())
	}

	time.Sleep(250 * time.Millisecond)

	if debouncer.GetPendingCount() != 0 {
		t.Error("Expected 0 pending messages after timeout")
	}
}

func TestDebouncer_ConcurrentAccess(t *testing.T) {
	debouncer := NewDebouncer(100 * time.Millisecond)
	defer debouncer.Stop()

	var wg sync.WaitGroup
	count := 100

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chatID := int64(i % 10) // 10 different chats
			debouncer.Debounce(chatID, "msg", func(string) {})
		}(i)
	}

	wg.Wait()
	// Should not panic
}
