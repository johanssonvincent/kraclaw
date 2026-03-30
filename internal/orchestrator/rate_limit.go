package orchestrator

import (
	"math"
	"sync"
	"time"
)

// TokenBucket implements a per-group token bucket rate limiter.
// Tokens refill at capacity-per-second continuously; excess is capped at capacity.
// TryAcquire is safe for concurrent use.
type TokenBucket struct {
	capacity     int64
	tokens       float64
	lastRefillAt time.Time
	mu           sync.Mutex
}

// newTokenBucket creates a TokenBucket with the given tokens-per-second capacity.
// The bucket starts full.
func newTokenBucket(capacity int64) *TokenBucket {
	return &TokenBucket{
		capacity:     capacity,
		tokens:       float64(capacity),
		lastRefillAt: time.Now(),
	}
}

// TryAcquire attempts to consume one token, refilling based on elapsed time first.
// Returns true if a token was available, false if the bucket is empty (drop the message).
func (tb *TokenBucket) TryAcquire(now time.Time) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	elapsed := now.Sub(tb.lastRefillAt).Seconds()
	tb.tokens = math.Min(float64(tb.capacity), tb.tokens+elapsed*float64(tb.capacity))
	tb.lastRefillAt = now

	if tb.tokens >= 1.0 {
		tb.tokens -= 1.0
		return true
	}
	return false
}
