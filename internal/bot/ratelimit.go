package bot

import (
	"sync"
	"time"
)

// RateLimiter implements a sliding window rate limiter per chat
type RateLimiter struct {
	maxRequests int
	window      time.Duration
	mu          sync.Mutex
	requests    map[int64][]time.Time
}

// NewRateLimiter creates a new rate limiter
// maxRequests: maximum number of requests allowed within the window
// window: time window for rate limiting (e.g., 1 minute)
func NewRateLimiter(maxRequests int, window time.Duration) *RateLimiter {
	if maxRequests <= 0 {
		maxRequests = 10 // default: 10 requests
	}
	if window <= 0 {
		window = 1 * time.Minute // default: 1 minute
	}
	return &RateLimiter{
		maxRequests: maxRequests,
		window:      window,
		requests:    make(map[int64][]time.Time),
	}
}

// Allow checks if a request from the given chat ID is allowed
// Returns true if the request is within rate limits, false otherwise
func (r *RateLimiter) Allow(chatID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()

	// Get request history for this chat
	history := r.requests[chatID]

	// Remove requests outside the window
	cutoff := now.Add(-r.window)
	validRequests := make([]time.Time, 0, len(history))
	for _, reqTime := range history {
		if reqTime.After(cutoff) {
			validRequests = append(validRequests, reqTime)
		}
	}

	// Check if limit exceeded
	if len(validRequests) >= r.maxRequests {
		r.requests[chatID] = validRequests
		return false
	}

	// Add current request
	validRequests = append(validRequests, now)
	r.requests[chatID] = validRequests

	return true
}

// RemainingCooldown returns the duration until the next request is allowed
// Returns 0 if a request is currently allowed
func (r *RateLimiter) RemainingCooldown(chatID int64) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	history := r.requests[chatID]

	if len(history) == 0 {
		return 0
	}

	// Remove requests outside the window
	cutoff := now.Add(-r.window)
	validRequests := make([]time.Time, 0, len(history))
	for _, reqTime := range history {
		if reqTime.After(cutoff) {
			validRequests = append(validRequests, reqTime)
		}
	}

	// If under limit, no cooldown
	if len(validRequests) < r.maxRequests {
		return 0
	}

	// Calculate when the oldest request will expire
	oldest := validRequests[0]
	expiresAt := oldest.Add(r.window)
	remaining := expiresAt.Sub(now)

	if remaining < 0 {
		return 0
	}

	return remaining
}

// GetRequestCount returns the current number of requests in the window for a chat
func (r *RateLimiter) GetRequestCount(chatID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	history := r.requests[chatID]
	cutoff := now.Add(-r.window)

	count := 0
	for _, reqTime := range history {
		if reqTime.After(cutoff) {
			count++
		}
	}

	return count
}

// Reset clears the request history for a specific chat
func (r *RateLimiter) Reset(chatID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.requests, chatID)
}

// ResetAll clears all request history
func (r *RateLimiter) ResetAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = make(map[int64][]time.Time)
}
