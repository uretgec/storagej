package httpapi

import (
	"sync"
	"time"
)

// RateLimiter is a per-tenant token-bucket rate limiter: each tenant gets
// its own bucket of capacity burst, refilling at ratePerSecond tokens per
// second. In-memory, no external dependency (deliberately not
// golang.org/x/time/rate — a per-tenant map around a plain token bucket is
// about thirty lines and doesn't need a new module for that).
//
// Resets on restart, and — like the per-tenant repository cache in
// MountMultiTenant — a bucket is never evicted once created. Fine for a
// bounded set of real tenants; combined with MountOptions.Auth, only
// authenticated tenants can create one in the first place.
type RateLimiter struct {
	ratePerSecond float64
	burst         float64

	mu      sync.Mutex
	buckets map[string]*tokenBucket
}

type tokenBucket struct {
	tokens   float64
	lastFill time.Time
}

// NewRateLimiter returns a RateLimiter allowing burst requests immediately
// per tenant, refilling at ratePerSecond tokens/sec after that.
func NewRateLimiter(ratePerSecond float64, burst int) *RateLimiter {
	return &RateLimiter{
		ratePerSecond: ratePerSecond,
		burst:         float64(burst),
		buckets:       make(map[string]*tokenBucket),
	}
}

// Allow reports whether tenant may proceed right now. If so, it consumes
// one token from that tenant's bucket; if not, the caller should reject
// the request (e.g. HTTP 429) without any other side effect.
func (rl *RateLimiter) Allow(tenant string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[tenant]
	if !ok {
		// A brand-new tenant starts with a full bucket minus the token
		// this call is about to spend.
		rl.buckets[tenant] = &tokenBucket{tokens: rl.burst - 1, lastFill: now}
		return true
	}

	elapsed := now.Sub(b.lastFill).Seconds()
	b.tokens += elapsed * rl.ratePerSecond
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.lastFill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
