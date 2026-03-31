# Coding: Complex Feature Implementation

## Description

Implement a generic rate-limiter in Go using a token bucket algorithm.
Requirements:
- `RateLimiter` struct with `Allow() bool` method
- Configurable rate (tokens per second) and burst size
- Thread-safe implementation using sync primitives
- Constructor `NewRateLimiter(rate float64, burst int) *RateLimiter`

## Expected Output

A complete, compilable Go file implementing the rate limiter.
The implementation should use `sync.Mutex` or `sync/atomic` for concurrency safety.
`Allow()` returns `true` if the request is within rate limits, `false` otherwise.

## Scoring Rubric

- Correct token bucket semantics (fill rate + burst cap)
- Thread-safe: uses sync primitives correctly
- `Allow()` is non-blocking
- Includes a constructor
- Verification: parallel calls to `Allow()` do not race
