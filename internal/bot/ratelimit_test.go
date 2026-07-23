package bot

import (
	"sync"
	"testing"
	"time"
)

// fakeClock is a controllable time source for deterministic rate-limiter tests.
// It removes the dependency on real wall-clock sleeps, which made the timing
// tests flaky under CPU load (the sleeps could overshoot the window).
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// newTestLimiter builds a RateLimiter wired to a fakeClock so tests can advance
// time explicitly instead of sleeping.
func newTestLimiter(maxRequests int, window time.Duration) (*RateLimiter, *fakeClock) {
	limiter := NewRateLimiter(maxRequests, window)
	clock := &fakeClock{now: time.Unix(0, 0)}
	limiter.now = clock.Now
	return limiter, clock
}

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
	limiter, clock := newTestLimiter(2, 200*time.Millisecond)

	chatID := int64(123)

	limiter.Allow(chatID)
	limiter.Allow(chatID)

	// Should be denied immediately
	if limiter.Allow(chatID) {
		t.Error("Request should be denied when limit reached")
	}

	// Advance past the window so the earlier requests expire
	clock.Advance(250 * time.Millisecond)

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
	limiter, clock := newTestLimiter(2, 500*time.Millisecond)

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

	// Advance and check again
	clock.Advance(250 * time.Millisecond)
	cooldown = limiter.RemainingCooldown(chatID)
	if cooldown <= 0 || cooldown > 300*time.Millisecond {
		t.Errorf("Expected reduced cooldown, got %v", cooldown)
	}

	// Advance past full expiry
	clock.Advance(300 * time.Millisecond)
	if cooldown := limiter.RemainingCooldown(chatID); cooldown != 0 {
		t.Errorf("Expected 0 cooldown after expiry, got %v", cooldown)
	}
}

func TestRateLimiter_GetRequestCount(t *testing.T) {
	limiter, clock := newTestLimiter(5, 500*time.Millisecond)

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

	clock.Advance(550 * time.Millisecond)

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
	limiter, clock := newTestLimiter(3, 300*time.Millisecond)

	chatID := int64(123)

	// Use all 3 requests
	limiter.Allow(chatID) // t=0
	clock.Advance(100 * time.Millisecond)
	limiter.Allow(chatID) // t=100
	clock.Advance(100 * time.Millisecond)
	limiter.Allow(chatID) // t=200

	// Should be denied — all three requests are still within the 300ms window
	if limiter.Allow(chatID) {
		t.Error("Should be denied at t=200")
	}

	// Advance so the first request (t=0) expires at t=300
	clock.Advance(150 * time.Millisecond) // Now at t=350

	// Should be allowed (first request expired)
	if !limiter.Allow(chatID) {
		t.Error("Should be allowed at t=350")
	}
}
