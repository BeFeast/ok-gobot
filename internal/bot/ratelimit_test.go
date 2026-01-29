package bot

import (
	"sync"
	"testing"
	"time"
)

func TestRateLimiter_Allow_UnderLimit(t *testing.T) {
	limiter := NewRateLimiter(3, 1*time.Second)

	chatID := int64(123)

	if !limiter.Allow(chatID) {
		t.Error("First request should be allowed")
	}
	if !limiter.Allow(chatID) {
		t.Error("Second request should be allowed")
	}
	if !limiter.Allow(chatID) {
		t.Error("Third request should be allowed")
	}
}

func TestRateLimiter_Allow_ExceedLimit(t *testing.T) {
	limiter := NewRateLimiter(2, 1*time.Second)

	chatID := int64(123)

	limiter.Allow(chatID)
	limiter.Allow(chatID)

	if limiter.Allow(chatID) {
		t.Error("Third request should be denied")
	}
}

func TestRateLimiter_Allow_WindowExpiry(t *testing.T) {
	limiter := NewRateLimiter(2, 200*time.Millisecond)

	chatID := int64(123)

	limiter.Allow(chatID)
	limiter.Allow(chatID)

	// Should be denied immediately
	if limiter.Allow(chatID) {
		t.Error("Request should be denied when limit reached")
	}

	// Wait for window to expire
	time.Sleep(250 * time.Millisecond)

	// Should be allowed again
	if !limiter.Allow(chatID) {
		t.Error("Request should be allowed after window expiry")
	}
}

func TestRateLimiter_MultipleChatsSeparate(t *testing.T) {
	limiter := NewRateLimiter(2, 1*time.Second)

	chat1 := int64(123)
	chat2 := int64(456)

	limiter.Allow(chat1)
	limiter.Allow(chat1)

	// Chat 1 should be at limit
	if limiter.Allow(chat1) {
		t.Error("Chat 1 should be rate limited")
	}

	// Chat 2 should still be allowed
	if !limiter.Allow(chat2) {
		t.Error("Chat 2 should be allowed (independent limit)")
	}
}

func TestRateLimiter_RemainingCooldown(t *testing.T) {
	limiter := NewRateLimiter(2, 500*time.Millisecond)

	chatID := int64(123)

	// No cooldown initially
	if cooldown := limiter.RemainingCooldown(chatID); cooldown != 0 {
		t.Errorf("Expected 0 cooldown initially, got %v", cooldown)
	}

	// Use up the limit
	limiter.Allow(chatID)
	limiter.Allow(chatID)

	// Should have cooldown
	cooldown := limiter.RemainingCooldown(chatID)
	if cooldown <= 0 || cooldown > 500*time.Millisecond {
		t.Errorf("Expected cooldown between 0 and 500ms, got %v", cooldown)
	}

	// Wait and check again
	time.Sleep(250 * time.Millisecond)
	cooldown = limiter.RemainingCooldown(chatID)
	if cooldown <= 0 || cooldown > 300*time.Millisecond {
		t.Errorf("Expected reduced cooldown, got %v", cooldown)
	}

	// Wait for full expiry
	time.Sleep(300 * time.Millisecond)
	if cooldown := limiter.RemainingCooldown(chatID); cooldown != 0 {
		t.Errorf("Expected 0 cooldown after expiry, got %v", cooldown)
	}
}

func TestRateLimiter_GetRequestCount(t *testing.T) {
	limiter := NewRateLimiter(5, 500*time.Millisecond)

	chatID := int64(123)

	if count := limiter.GetRequestCount(chatID); count != 0 {
		t.Errorf("Expected 0 requests initially, got %d", count)
	}

	limiter.Allow(chatID)
	limiter.Allow(chatID)
	limiter.Allow(chatID)

	if count := limiter.GetRequestCount(chatID); count != 3 {
		t.Errorf("Expected 3 requests, got %d", count)
	}

	time.Sleep(550 * time.Millisecond)

	if count := limiter.GetRequestCount(chatID); count != 0 {
		t.Errorf("Expected 0 requests after expiry, got %d", count)
	}
}

func TestRateLimiter_Reset(t *testing.T) {
	limiter := NewRateLimiter(2, 1*time.Second)

	chatID := int64(123)

	limiter.Allow(chatID)
	limiter.Allow(chatID)

	// Should be at limit
	if limiter.Allow(chatID) {
		t.Error("Should be at limit before reset")
	}

	// Reset and try again
	limiter.Reset(chatID)

	if !limiter.Allow(chatID) {
		t.Error("Should be allowed after reset")
	}
}

func TestRateLimiter_ResetAll(t *testing.T) {
	limiter := NewRateLimiter(1, 1*time.Second)

	chat1 := int64(123)
	chat2 := int64(456)

	limiter.Allow(chat1)
	limiter.Allow(chat2)

	// Both should be at limit
	if limiter.Allow(chat1) {
		t.Error("Chat 1 should be at limit")
	}
	if limiter.Allow(chat2) {
		t.Error("Chat 2 should be at limit")
	}

	// Reset all
	limiter.ResetAll()

	if !limiter.Allow(chat1) {
		t.Error("Chat 1 should be allowed after reset")
	}
	if !limiter.Allow(chat2) {
		t.Error("Chat 2 should be allowed after reset")
	}
}

func TestRateLimiter_DefaultValues(t *testing.T) {
	limiter := NewRateLimiter(0, 0)

	if limiter.maxRequests != 10 {
		t.Errorf("Expected default maxRequests of 10, got %d", limiter.maxRequests)
	}
	if limiter.window != 1*time.Minute {
		t.Errorf("Expected default window of 1 minute, got %v", limiter.window)
	}
}

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	limiter := NewRateLimiter(100, 1*time.Second)

	var wg sync.WaitGroup
	count := 1000

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			chatID := int64(i % 10) // 10 different chats
			limiter.Allow(chatID)
			limiter.GetRequestCount(chatID)
			limiter.RemainingCooldown(chatID)
		}(i)
	}

	wg.Wait()
	// Should not panic or race
}

func TestRateLimiter_SlidingWindow(t *testing.T) {
	limiter := NewRateLimiter(3, 300*time.Millisecond)

	chatID := int64(123)

	// Use all 3 requests
	limiter.Allow(chatID) // t=0
	time.Sleep(100 * time.Millisecond)
	limiter.Allow(chatID) // t=100
	time.Sleep(100 * time.Millisecond)
	limiter.Allow(chatID) // t=200

	// Should be denied
	if limiter.Allow(chatID) {
		t.Error("Should be denied at t=200")
	}

	// Wait for first request to expire (at t=300)
	time.Sleep(150 * time.Millisecond) // Now at t=350

	// Should be allowed (first request expired)
	if !limiter.Allow(chatID) {
		t.Error("Should be allowed at t=350")
	}
}
