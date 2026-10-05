package limit

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// miniredis is a Redis server that runs inside the test process, so these tests need
// no Docker and no network.

// newFixedWindows returns n limiters ("replicas") sharing one in-process Redis and one
// fake clock, set to the start of a window.
func newFixedWindows(t *testing.T, n int, rate float64, window time.Duration) ([]*FixedWindow, *fakeClock, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	var fws []*FixedWindow
	for range n {
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1}) // fail fast when Redis is down
		t.Cleanup(func() { rdb.Close() })
		fw, err := NewFixedWindow(rdb, rate, window)
		if err != nil {
			t.Fatal(err)
		}
		fw.now = clock.Now
		fws = append(fws, fw)
	}
	return fws, clock, mr
}

func TestNewFixedWindowValidates(t *testing.T) {
	for _, tc := range []struct {
		rate   float64
		window time.Duration
	}{{0.01, 10 * time.Second}, {1, 0}, {0, time.Second}} {
		if _, err := NewFixedWindow(nil, tc.rate, tc.window); err == nil {
			t.Errorf("NewFixedWindow(rate %v, window %v) returned no error", tc.rate, tc.window)
		}
	}
}

func TestFixedWindowSteps(t *testing.T) {
	fws, clock, _ := newFixedWindows(t, 1, 0.3, 10*time.Second) // 3 requests per 10s
	fw := fws[0]

	steps := []struct {
		name          string
		advance       time.Duration
		wantAllowed   bool
		wantRemaining int
		wantRetry     time.Duration
	}{
		{"1 of 3", 0, true, 2, 0},
		{"2 of 3", 0, true, 1, 0},
		{"3 of 3", 0, true, 0, 0},
		{"over: retry when the window ends", 0, false, 0, 10 * time.Second},
		{"4s later, still the same window", 4 * time.Second, false, 0, 6 * time.Second},
		{"next window starts fresh", 6 * time.Second, true, 2, 0},
	}
	for _, s := range steps {
		clock.Advance(s.advance)
		d, err := fw.Allow(context.Background(), "1.2.3.4")
		if err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if d.Allowed != s.wantAllowed || d.Remaining != s.wantRemaining || d.RetryAfter != s.wantRetry || d.Limit != 3 {
			t.Errorf("%s: got %+v, want allowed=%v remaining=%d retry=%v limit=3",
				s.name, d, s.wantAllowed, s.wantRemaining, s.wantRetry)
		}
	}
}

// Every counter must expire, or Redis fills up with one key per client per window.
func TestFixedWindowKeysExpire(t *testing.T) {
	fws, _, mr := newFixedWindows(t, 1, 1, 10*time.Second)
	fws[0].Allow(context.Background(), "1.2.3.4")

	keys := mr.Keys()
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "rl:fw:1.2.3.4:") {
		t.Fatalf("keys = %v, want one rl:fw:1.2.3.4:<window> key", keys)
	}
	if ttl := mr.TTL(keys[0]); ttl != 10*time.Second {
		t.Errorf("TTL = %v, want 10s", ttl)
	}
	mr.FastForward(10 * time.Second)
	if keys := mr.Keys(); len(keys) != 0 {
		t.Errorf("after the TTL, keys = %v, want none", keys)
	}
}

// Two replicas, requests alternating between them: the limit holds for the client as a
// whole. With an in-memory limiter each replica would allow the full limit.
func TestFixedWindowSharedAcrossReplicas(t *testing.T) {
	fws, _, _ := newFixedWindows(t, 2, 1, 10*time.Second) // 10 per 10s

	allowed := 0
	for i := range 40 {
		d, err := fws[i%2].Allow(context.Background(), "1.2.3.4")
		if err != nil {
			t.Fatal(err)
		}
		if d.Allowed {
			allowed++
		}
	}
	if allowed != 10 {
		t.Errorf("allowed %d of 40 across two replicas, want 10", allowed)
	}
}

// 100 requests at once across two replicas. INCR is atomic in Redis, so exactly the
// limit gets through.
func TestFixedWindowConcurrentNeverOverAdmits(t *testing.T) {
	fws, _, _ := newFixedWindows(t, 2, 1, 10*time.Second)

	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := range 100 {
		wg.Go(func() {
			if d, err := fws[i%2].Allow(context.Background(), "1.2.3.4"); err == nil && d.Allowed {
				allowed.Add(1)
			}
		})
	}
	wg.Wait()
	if got := allowed.Load(); got != 10 {
		t.Errorf("allowed %d of 100 concurrent requests, want exactly 10", got)
	}
}

// The known flaw, kept as a test so it stays documented: 10 requests just before a
// boundary and 10 just after are all allowed, 20 within 0.2s for a 10-per-10s limit.
func TestFixedWindowBoundaryBurst(t *testing.T) {
	fws, clock, _ := newFixedWindows(t, 1, 1, 10*time.Second)
	ctx := context.Background()

	allowed := 0
	clock.Advance(9900 * time.Millisecond) // 0.1s before the boundary
	for range 10 {
		if d, _ := fws[0].Allow(ctx, "1.2.3.4"); d.Allowed {
			allowed++
		}
	}
	clock.Advance(200 * time.Millisecond) // 0.1s after it
	for range 10 {
		if d, _ := fws[0].Allow(ctx, "1.2.3.4"); d.Allowed {
			allowed++
		}
	}
	if allowed != 20 {
		t.Errorf("allowed %d in 0.2s around the boundary; the fixed window is expected to allow 20", allowed)
	}
}

func TestFixedWindowRedisDown(t *testing.T) {
	fws, _, mr := newFixedWindows(t, 1, 1, 10*time.Second)
	mr.Close()

	if _, err := fws[0].Allow(context.Background(), "1.2.3.4"); err == nil {
		t.Error("Allow with Redis down returned no error")
	}
}
