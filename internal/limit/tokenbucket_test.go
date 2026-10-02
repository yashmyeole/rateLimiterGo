package limit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a clock the test moves by hand, so refill math can be checked exactly
// without real waiting. The mutex lets a janitor goroutine read it while the test
// advances it.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestBucket(t *testing.T, rate float64, burst int) (*TokenBucket, *fakeClock) {
	t.Helper()
	tb, err := NewTokenBucket(rate, burst)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	tb.now = clock.Now
	return tb, clock
}

func TestNewTokenBucketValidates(t *testing.T) {
	for _, tc := range []struct {
		rate  float64
		burst int
	}{{0, 5}, {-1, 5}, {1, 0}} {
		if _, err := NewTokenBucket(tc.rate, tc.burst); err == nil {
			t.Errorf("NewTokenBucket(%v, %d) returned no error", tc.rate, tc.burst)
		}
	}
}

// Each step moves the clock, makes one request, and checks the decision. Steps build
// on each other, so they run in order.
func TestTokenBucketSteps(t *testing.T) {
	tb, clock := newTestBucket(t, 2, 3) // 2 tokens/s, bursts of 3

	steps := []struct {
		name          string
		advance       time.Duration
		key           string
		wantAllowed   bool
		wantRemaining int
		wantRetry     time.Duration
	}{
		{"new client starts full", 0, "a", true, 2, 0},
		{"burst 2 of 3", 0, "a", true, 1, 0},
		{"burst 3 of 3", 0, "a", true, 0, 0},
		{"empty: retry when one token has dripped in", 0, "a", false, 0, 500 * time.Millisecond},
		{"half a token later, still short", 250 * time.Millisecond, "a", false, 0, 250 * time.Millisecond},
		{"one whole token later, allowed", 250 * time.Millisecond, "a", true, 0, 0},
		{"that token is spent: empty again", 0, "a", false, 0, 500 * time.Millisecond}, // fails if refill is counted twice
		{"other clients have their own bucket", 0, "b", true, 2, 0},
		{"an hour idle refills only to burst", time.Hour, "a", true, 2, 0},
	}

	for _, s := range steps {
		clock.Advance(s.advance)
		d, err := tb.Allow(context.Background(), s.key)
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if d.Allowed != s.wantAllowed || d.Remaining != s.wantRemaining || d.RetryAfter != s.wantRetry || d.Limit != 3 {
			t.Errorf("%s: got %+v, want allowed=%v remaining=%d retry=%v limit=3",
				s.name, d, s.wantAllowed, s.wantRemaining, s.wantRetry)
		}
	}
}

// With the clock frozen, 50 goroutines racing for one key's 10 tokens must win exactly
// 10 times. Without the mutex this over-admits and `go test -race` reports it.
func TestTokenBucketConcurrentAllowNeverOverAdmits(t *testing.T) {
	tb, _ := newTestBucket(t, 1, 10)

	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if d, _ := tb.Allow(context.Background(), "same-client"); d.Allowed {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()

	if got := allowed.Load(); got != 10 {
		t.Errorf("allowed %d of 50 concurrent requests, want exactly 10", got)
	}
}
