package security

import (
	"sync"
	"time"
)

// RateLimiter is a simple in-memory token bucket per key (usually client IP).
// Good for a single API instance; move to Redis or Postgres if you run several.
type RateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens added per second
	burst   float64
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter allows `perMinute` requests per minute with bursts up to `burst`.
func NewRateLimiter(perMinute, burst int) *RateLimiter {
	rl := &RateLimiter{
		rate:    float64(perMinute) / 60.0,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
	}
	go rl.cleanup()
	return rl
}

// Allow reports whether a request for key may proceed, and if not, how long to wait.
func (rl *RateLimiter) Allow(key string) (bool, time.Duration) {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / rl.rate * float64(time.Second))
	return false, wait
}

func (rl *RateLimiter) cleanup() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for range t.C {
		cutoff := time.Now().Add(-15 * time.Minute)
		rl.mu.Lock()
		for k, b := range rl.buckets {
			if b.last.Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
		rl.mu.Unlock()
	}
}
