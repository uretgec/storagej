package httpapi

import (
	"sync"
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToBurst(t *testing.T) {
	rl := NewRateLimiter(0, 3) // no refill: exactly 3 allowed, ever

	for i := 0; i < 3; i++ {
		if !rl.Allow("t") {
			t.Fatalf("call %d should be within burst", i)
		}
	}
	if rl.Allow("t") {
		t.Fatal("4th call should exceed burst")
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(1000, 1) // fast refill so the test doesn't need a real sleep

	if !rl.Allow("t") {
		t.Fatal("1st call should be allowed")
	}
	if rl.Allow("t") {
		t.Fatal("2nd call should be blocked before any refill")
	}

	time.Sleep(5 * time.Millisecond)

	if !rl.Allow("t") {
		t.Fatal("call after the refill window should be allowed")
	}
}

// TestRateLimiterRefillNeverExceedsBurst confirms an idle bucket's refill
// is capped at burst, not unbounded. This checks the bucket's internal
// token count directly (legal here — same-package test) rather than
// counting how many rapid Allow() calls succeed: at an extreme rate like
// this, even the nanoseconds of real elapsed time between tight-loop
// iterations are enough to fully refill the bucket every single call,
// which made an earlier version of this test measure loop overhead
// instead of the capping logic.
func TestRateLimiterRefillNeverExceedsBurst(t *testing.T) {
	rl := NewRateLimiter(1e9, 2) // absurdly fast refill

	rl.Allow("t") // creates the bucket (tokens = burst-1 = 1)

	// Simulate a long idle period deterministically, rather than sleeping
	// and hoping real elapsed time lands in some particular range.
	rl.mu.Lock()
	rl.buckets["t"].lastFill = time.Now().Add(-time.Hour)
	rl.mu.Unlock()

	if !rl.Allow("t") {
		t.Fatal("call after a long idle period should be allowed")
	}

	rl.mu.Lock()
	tokens := rl.buckets["t"].tokens
	rl.mu.Unlock()

	if tokens > rl.burst {
		t.Fatalf("tokens = %v after idle refill, want <= burst (%v) — refill must be capped", tokens, rl.burst)
	}
}

func TestRateLimiterIsPerTenant(t *testing.T) {
	rl := NewRateLimiter(0, 1)

	if !rl.Allow("acme") {
		t.Fatal("acme's first call should be allowed")
	}
	if rl.Allow("acme") {
		t.Fatal("acme's second call should be blocked")
	}
	if !rl.Allow("other") {
		t.Fatal("other's first call should be allowed regardless of acme's state")
	}
}

// TestRateLimiterConcurrentAccessIsRaceFree exercises Allow from many
// goroutines against a handful of shared tenant keys — the scenario that
// would trip the race detector if the buckets map or a bucket's fields
// weren't properly protected by the mutex. Run with -race.
func TestRateLimiterConcurrentAccessIsRaceFree(t *testing.T) {
	rl := NewRateLimiter(1000, 5)
	tenants := []string{"a", "b", "c"}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rl.Allow(tenants[i%len(tenants)])
		}(i)
	}
	wg.Wait()
}
