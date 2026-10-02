package limit

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"sync"
	"time"
)

var _ Limiter = (*TokenBucket)(nil)

// TokenBucket gives each key a bucket holding up to burst tokens that refills at rate
// tokens per second. Each request spends one token; an empty bucket means 429. Clients
// can burst up to burst requests, then sustain rate requests per second on average.
type TokenBucket struct {
	rate  float64          // tokens added per second
	burst float64          // bucket capacity
	now   func() time.Time // time.Now, swapped for a fake clock in tests

	mu      sync.Mutex // guards the fields below and every bucket in the map
	buckets map[string]*bucket
	peak    int // most buckets the map has held since it was last rebuilt
}

type bucket struct {
	tokens float64   // fractional: refill adds rate*seconds, which is rarely whole
	last   time.Time // when tokens was last brought up to date
}

// NewTokenBucket returns a limiter allowing rate requests per second per key, with
// bursts of up to burst requests. Run RunJanitor alongside it, or the map keeps a
// bucket for every client ever seen.
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
		tb.peak = max(tb.peak, len(tb.buckets))
	}
	return b.take(now, tb.rate, tb.burst), nil
}

// RunJanitor deletes idle buckets every interval until ctx is canceled. It blocks, so
// start it with `go tb.RunJanitor(ctx, interval)`.
func (tb *TokenBucket) RunJanitor(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if evicted, left := tb.evictIdle(); evicted > 0 {
				slog.Info("rate limiter evicted idle clients", "evicted", evicted, "clients", left)
			}
		}
	}
}

// evictIdle deletes every bucket that has refilled completely. A full bucket behaves
// exactly like a brand-new one, so deleting it never changes a decision; it only
// frees memory. It holds the lock for the whole scan, which pauses requests briefly.
func (tb *TokenBucket) evictIdle() (evicted, left int) {
	now := tb.now()

	tb.mu.Lock()
	defer tb.mu.Unlock()

	for key, b := range tb.buckets {
		if b.fullAt(now, tb.rate, tb.burst) {
			delete(tb.buckets, key) // deleting during range is allowed in Go
			evicted++
		}
	}

	// A Go map never hands memory back when keys are deleted: after a spike of a million
	// clients it stays sized for a million. Once it has shrunk to a quarter of its peak,
	// copy the survivors into a fresh map so the old one can be freed.
	if len(tb.buckets) < tb.peak/4 {
		fresh := make(map[string]*bucket, len(tb.buckets))
		maps.Copy(fresh, tb.buckets)
		tb.buckets = fresh
		tb.peak = len(fresh)
	}
	return evicted, len(tb.buckets)
}

// take brings b up to date, then spends one token if there is one.
//
// Lazy refill: rather than a timer per client topping buckets up, work out how many
// tokens dripped in since b was last touched. Idle clients cost nothing, and the
// result is the same as if a timer had been running all along.
func (b *bucket) take(now time.Time, rate, burst float64) Decision {
	b.tokens = min(burst, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now

	d := Decision{Limit: int(burst)}
	if b.tokens >= 1 {
		b.tokens--
		d.Allowed = true
		d.Remaining = int(b.tokens)
		return d
	}
	// Short by (1 - tokens), which arrives at rate tokens per second.
	d.RetryAfter = time.Duration((1 - b.tokens) / rate * float64(time.Second))
	return d
}

// fullAt reports whether b would be full at now, without changing it.
func (b *bucket) fullAt(now time.Time, rate, burst float64) bool {
	return b.tokens+now.Sub(b.last).Seconds()*rate >= burst
}
