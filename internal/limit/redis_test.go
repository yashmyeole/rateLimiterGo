package limit

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// redisClock moves an in-process Redis's clock (what the scripts read with TIME) and
// its key expiry together.
type redisClock struct {
	mr *miniredis.Miniredis
	t  time.Time
}

func (c *redisClock) Advance(d time.Duration) {
	c.t = c.t.Add(d)
	c.mr.SetTime(c.t)
	c.mr.FastForward(d)
}

// newRedis starts an in-process Redis at a fixed time and returns n clients for it, one
// per simulated proxy replica.
func newRedis(t *testing.T, n int) (*redisClock, []*redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	clock := &redisClock{mr: mr, t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	mr.SetTime(clock.t)

	var clients []*redis.Client
	for range n {
		// No retries: a test with Redis down should fail fast, not back off for seconds.
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
		t.Cleanup(func() { rdb.Close() })
		clients = append(clients, rdb)
	}
	return clock, clients
}

// concurrentAllowed sends n requests for one key at once, spread across the limiters,
// and counts how many were allowed.
func concurrentAllowed(t *testing.T, limiters []Limiter, n int) int {
	t.Helper()
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			d, err := limiters[i%len(limiters)].Allow(context.Background(), "1.2.3.4")
			if err != nil {
				t.Error(err)
				return
			}
			if d.Allowed {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	return int(allowed.Load())
}

func TestNewRedisLimitersValidate(t *testing.T) {
	if _, err := NewRedisTokenBucket(nil, 0, 5); err == nil {
		t.Error("NewRedisTokenBucket(rate 0) returned no error")
	}
	if _, err := NewRedisTokenBucket(nil, 1, 0); err == nil {
		t.Error("NewRedisTokenBucket(burst 0) returned no error")
	}
	if _, err := NewSlidingWindow(nil, 0.01, 10*time.Second); err == nil {
		t.Error("NewSlidingWindow(limit below 1) returned no error")
	}
}

// The Lua token bucket and the in-memory Go one get the same random traffic on the same
// clock. Every decision must match, which shows the Lua port behaves like the original.
func TestRedisTokenBucketMatchesInMemory(t *testing.T) {
	clock, clients := newRedis(t, 1)
	lua, err := NewRedisTokenBucket(clients[0], 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	mem, memClock := newTestBucket(t, 2, 3)
	memClock.t = clock.t

	ctx := context.Background()
	rng := rand.New(rand.NewPCG(3, 4)) // fixed seed: the same traffic on every run
	var allowed, denied int
	for i := range 600 {
		d := time.Duration(rng.N(150)) * time.Millisecond
		if rng.N(50) == 0 {
			d += 3 * time.Second // long enough for buckets to refill and Redis keys to expire
		}
		clock.Advance(d)
		memClock.Advance(d)

		key := fmt.Sprintf("client-%d", rng.N(8))
		want, _ := mem.Allow(ctx, key)
		got, err := lua.Allow(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		// Lua reports Retry-After in whole ms (rounded up), Go in ns.
		wantRetry := want.RetryAfter.Round(time.Millisecond)
		if got.Allowed != want.Allowed || got.Remaining != want.Remaining || got.Limit != want.Limit ||
			(got.RetryAfter-wantRetry).Abs() > time.Millisecond {
			t.Fatalf("step %d, %s: Lua %+v, in-memory %+v", i, key, got, want)
		}
		if got.Allowed {
			allowed++
		} else {
			denied++
		}
	}
	if allowed == 0 || denied == 0 {
		t.Errorf("weak run: allowed %d, denied %d; want both > 0", allowed, denied)
	}
}

// Two replicas, 100 requests at once, Redis's clock frozen: exactly the burst gets through.
func TestRedisTokenBucketConcurrentNeverOverAdmits(t *testing.T) {
	_, clients := newRedis(t, 2)
	var limiters []Limiter
	for _, rdb := range clients {
		tb, err := NewRedisTokenBucket(rdb, 1, 10)
		if err != nil {
			t.Fatal(err)
		}
		limiters = append(limiters, tb)
	}
	if got := concurrentAllowed(t, limiters, 100); got != 10 {
		t.Errorf("allowed %d of 100 concurrent requests across two replicas, want exactly 10", got)
	}
}

func TestRedisTokenBucketKeyExpires(t *testing.T) {
	clock, clients := newRedis(t, 1)
	tb, err := NewRedisTokenBucket(clients[0], 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	tb.Allow(context.Background(), "1.2.3.4")

	if ttl := clock.mr.TTL("rl:tb:{1.2.3.4}"); ttl != 2*time.Second {
		t.Errorf("TTL = %v, want 2s (the time an idle bucket takes to refill)", ttl)
	}
}

// redis.Script sends only the script's hash; if Redis no longer has the script (after a
// restart or SCRIPT FLUSH) it must resend the source instead of failing.
func TestScriptsSurviveScriptFlush(t *testing.T) {
	_, clients := newRedis(t, 1)
	ctx := context.Background()
	tb, _ := NewRedisTokenBucket(clients[0], 10, 20)
	sw, _ := NewSlidingWindow(clients[0], 1, 10*time.Second)

	for _, l := range []Limiter{tb, sw} {
		if _, err := l.Allow(ctx, "1.2.3.4"); err != nil {
			t.Fatal(err)
		}
	}
	if err := clients[0].ScriptFlush(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	for _, l := range []Limiter{tb, sw} {
		if _, err := l.Allow(ctx, "1.2.3.4"); err != nil {
			t.Errorf("%T after SCRIPT FLUSH: %v", l, err)
		}
	}
}

func TestSlidingWindowSteps(t *testing.T) {
	clock, clients := newRedis(t, 1)
	sw, err := NewSlidingWindow(clients[0], 1, 4*time.Second) // 4 requests per 4s
	if err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		name          string
		advance       time.Duration
		wantAllowed   bool
		wantRemaining int
		wantRetry     time.Duration
	}{
		{"1 of 4", 0, true, 3, 0},
		{"2 of 4", 0, true, 2, 0},
		{"3 of 4", 0, true, 1, 0},
		{"4 of 4", 0, true, 0, 0},
		// Next window, 1s in: prev 4 weighted 0.75 = 3, plus this one = 4. Allowed at t=5s.
		{"window full: wait for the next one", 0, false, 0, 5 * time.Second},
		{"t=2s, same window", 2 * time.Second, false, 0, 3 * time.Second},
		// t=4.999s: prev 4 x 0.75025 = 3.001, +1 > 4.
		{"t=4.999s: previous window still weighs too much", 2999 * time.Millisecond, false, 0, time.Millisecond},
		{"t=5s: 4 x 0.75 + 1 = 4, allowed", time.Millisecond, true, 0, 0},
		{"t=6s: 4 x 0.5 + 1 + 1 = 4, allowed", time.Second, true, 0, 0},
		{"t=6s again: 2 + 2 + 1 = 5", 0, false, 0, time.Second},
		{"t=14s: a whole window skipped, nothing counts", 8 * time.Second, true, 3, 0},
	}
	for _, s := range steps {
		clock.Advance(s.advance)
		d, err := sw.Allow(context.Background(), "1.2.3.4")
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if d.Allowed != s.wantAllowed || d.Remaining != s.wantRemaining || d.RetryAfter != s.wantRetry || d.Limit != 4 {
			t.Errorf("%s: got %+v, want allowed=%v remaining=%d retry=%v limit=4",
				s.name, d, s.wantAllowed, s.wantRemaining, s.wantRetry)
		}
	}
}

// The fixed window allows 20 here (TestFixedWindowBoundaryBurst); the sliding window
// still counts the requests from just before the boundary.
func TestSlidingWindowNoBoundaryBurst(t *testing.T) {
	clock, clients := newRedis(t, 1)
	sw, err := NewSlidingWindow(clients[0], 1, 10*time.Second) // 10 per 10s
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	allowed := 0
	clock.Advance(9900 * time.Millisecond) // 0.1s before the boundary
	for range 10 {
		if d, _ := sw.Allow(ctx, "1.2.3.4"); d.Allowed {
			allowed++
		}
	}
	clock.Advance(200 * time.Millisecond) // 0.1s after it
	for range 10 {
		if d, _ := sw.Allow(ctx, "1.2.3.4"); d.Allowed {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("allowed %d in 0.2s around the boundary, want 10", allowed)
	}
}

func TestSlidingWindowConcurrentNeverOverAdmits(t *testing.T) {
	_, clients := newRedis(t, 2)
	var limiters []Limiter
	for _, rdb := range clients {
		sw, err := NewSlidingWindow(rdb, 1, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		limiters = append(limiters, sw)
	}
	if got := concurrentAllowed(t, limiters, 100); got != 10 {
		t.Errorf("allowed %d of 100 concurrent requests across two replicas, want exactly 10", got)
	}
}

func TestSlidingWindowKeyExpires(t *testing.T) {
	clock, clients := newRedis(t, 1)
	sw, _ := NewSlidingWindow(clients[0], 1, 10*time.Second)
	sw.Allow(context.Background(), "1.2.3.4")

	if ttl := clock.mr.TTL("rl:sw:{1.2.3.4}"); ttl != 20*time.Second {
		t.Errorf("TTL = %v, want 20s (two windows)", ttl)
	}
}

func TestRedisLimitersFailWhenRedisIsDown(t *testing.T) {
	clock, clients := newRedis(t, 1)
	tb, _ := NewRedisTokenBucket(clients[0], 10, 20)
	sw, _ := NewSlidingWindow(clients[0], 1, 10*time.Second)
	clock.mr.Close()

	for _, l := range []Limiter{tb, sw} {
		if _, err := l.Allow(context.Background(), "1.2.3.4"); err == nil {
			t.Errorf("%T with Redis down returned no error", l)
		}
	}
}
