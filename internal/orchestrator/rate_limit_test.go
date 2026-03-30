package orchestrator

import (
	"testing"
	"time"
)

func TestTokenBucket(t *testing.T) {
	t.Run("new bucket starts full", func(t *testing.T) {
		tb := newTokenBucket(10)
		if tb.capacity != 10 {
			t.Errorf("capacity = %d, want 10", tb.capacity)
		}
		if tb.tokens != 10 {
			t.Errorf("tokens = %f, want 10", tb.tokens)
		}
	})

	t.Run("TryAcquire succeeds for capacity calls", func(t *testing.T) {
		tb := newTokenBucket(10)
		now := time.Now()
		for i := 0; i < 10; i++ {
			if !tb.TryAcquire(now) {
				t.Fatalf("TryAcquire(%d) returned false, want true", i)
			}
		}
	})

	t.Run("TryAcquire fails after capacity exhausted", func(t *testing.T) {
		tb := newTokenBucket(10)
		now := time.Now()
		for i := 0; i < 10; i++ {
			tb.TryAcquire(now)
		}
		if tb.TryAcquire(now) {
			t.Fatal("TryAcquire(11) returned true, want false (bucket should be empty)")
		}
	})

	t.Run("tokens refill after elapsed time", func(t *testing.T) {
		tb := newTokenBucket(10)
		now := time.Now()
		// Drain all tokens.
		for i := 0; i < 10; i++ {
			tb.TryAcquire(now)
		}
		// After 1 second, 10 tokens should have refilled.
		later := now.Add(time.Second)
		if !tb.TryAcquire(later) {
			t.Fatal("TryAcquire after 1s refill returned false, want true")
		}
	})

	t.Run("tokens never exceed capacity", func(t *testing.T) {
		tb := newTokenBucket(10)
		now := time.Now()
		// Even after 100 seconds, tokens should be capped at capacity (10).
		later := now.Add(100 * time.Second)
		tb.TryAcquire(later) // trigger refill
		// After refill the bucket should have at most capacity - 1 (we consumed 1).
		// Drain to verify: should be able to get 9 more (total 10).
		for i := 0; i < 9; i++ {
			if !tb.TryAcquire(later) {
				t.Fatalf("TryAcquire(%d) after 100s returned false, want true", i+2)
			}
		}
		if tb.TryAcquire(later) {
			t.Fatal("TryAcquire(11) after 100s returned true, tokens should be capped at capacity")
		}
	})
}
