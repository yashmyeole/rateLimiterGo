package limit

import (
	"context"
	"errors"
	"sync"
	"time"
)

var _ Limiter = (*TokenBucket)(nil)

// TokenBucket gives each key a bucket holding up to burst tokens that refills at rate
// tokens per second. Each request spends one token; an empty bucket means 429. Clients
// can burst up to burst requests, then sustain rate requests per second on average.
//
// Buckets are never removed yet, so memory grows with every new client.
type TokenBucket struct {
	rate  float64          // tokens added per second
	burst float64          // bucket capacity
	now   func() time.Time // time.Now, swapped for a fake clock in tests

	mu      sync.Mutex // guards buckets and every bucket in it
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64   // fractional: refill adds rate*seconds, which is rarely whole
	last   time.Time // when tokens was last brought up to date
}

// NewTokenBucket returns a limiter allowing rate requests per second per key, with
// bursts of up to burst requests.
func NewTokenBucket(rate float64, burst int) (*TokenBucket, error) {
	if rate <= 0 || burst < 1 {
		return nil, errors.New("token bucket needs rate > 0 and burst >= 1")
	}
	return &TokenBucket{
		rate:    rate,
		burst:   float64(burst),
		now:     time.Now,
		buckets: make(map[string]*bucket),
	}, nil
}

// Allow spends one token from key's bucket if there is one.
func (tb *TokenBucket) Allow(_ context.Context, key string) (Decision, error) {
	now := tb.now()

	tb.mu.Lock()
	defer tb.mu.Unlock()

	b, ok := tb.buckets[key]
	if !ok {
		b = &bucket{tokens: tb.burst, last: now} // new clients start with a full bucket
		tb.buckets[key] = b
	}

	// Lazy refill: rather than a timer per client topping buckets up, work out how many
	// tokens dripped in since this bucket was last touched. Idle clients cost nothing,
	// and the result is the same as if a timer had been running all along.
	elapsed := now.Sub(b.last).Seconds()
	b.tokens = min(tb.burst, b.tokens+elapsed*tb.rate)
	b.last = now

	d := Decision{Limit: int(tb.burst)}
	if b.tokens >= 1 {
		b.tokens--
		d.Allowed = true
		d.Remaining = int(b.tokens)
		return d, nil
	}
	// Short by (1 - tokens), which arrives at rate tokens per second.
	d.RetryAfter = time.Duration((1 - b.tokens) / tb.rate * float64(time.Second))
	return d, nil
}
