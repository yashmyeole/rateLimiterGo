package limit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// fakeShared stands in for Redis. The test switches it between up, down (an instant
// error, like a refused connection) and hung (no answer until the deadline).
type fakeShared struct {
	calls      atomic.Int32
	down, hang atomic.Bool
}

func (f *fakeShared) Allow(ctx context.Context, _ string) (Decision, error) {
	f.calls.Add(1)
	switch {
	case f.hang.Load():
		<-ctx.Done()
		return Decision{}, ctx.Err()
	case f.down.Load():
		return Decision{}, errors.New("connection refused")
	}
	return Decision{Allowed: true, Limit: 100, Remaining: 99}, nil
}

// newTestResilient wraps a fakeShared; the local fallback has Limit 5, so a decision's
// Limit shows which limiter made it.
func newTestResilient(t *testing.T, mode FailMode) (*Resilient, *fakeShared, *fakeClock) {
	t.Helper()
	shared := &fakeShared{}
	local, _ := newTestBucket(t, 1, 5)
	r, err := NewResilient(shared, local, mode, 50*time.Millisecond, 3, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	r.breaker.now = clock.Now
	return r, shared, clock
}

func TestNewResilientValidates(t *testing.T) {
	shared, local := &fakeShared{}, &fakeShared{}
	if _, err := NewResilient(shared, nil, FallbackLocal, time.Millisecond, 3, time.Second); err == nil {
		t.Error("FallbackLocal without a local limiter: no error")
	}
	if _, err := NewResilient(shared, local, FailOpen, 0, 3, time.Second); err == nil {
		t.Error("zero budget: no error")
	}
	if _, err := NewResilient(shared, nil, FailClosed, time.Millisecond, 3, time.Second); err != nil {
		t.Errorf("FailClosed without a local limiter: %v", err)
	}
}

func TestResilientFallsBackAndRecovers(t *testing.T) {
	r, shared, clock := newTestResilient(t, FallbackLocal)
	ctx := context.Background()

	if d, err := r.Allow(ctx, "a"); err != nil || d.Limit != 100 {
		t.Fatalf("healthy: got %+v, %v; want the shared limiter's decision", d, err)
	}

	shared.down.Store(true)
	for i := range 3 { // each still tries Redis; the third failure opens the breaker
		if d, err := r.Allow(ctx, "a"); err != nil || d.Limit != 5 {
			t.Fatalf("failure %d: got %+v, %v; want the local fallback's decision", i+1, d, err)
		}
	}
	if d, _ := r.Allow(ctx, "a"); d.Limit != 5 || shared.calls.Load() != 4 {
		t.Fatalf("breaker open: Redis called %d times, want 4 (no call while open)", shared.calls.Load())
	}

	shared.down.Store(false)
	clock.Advance(2 * time.Second) // cooldown over: the next request is the trial
	if d, _ := r.Allow(ctx, "a"); d.Limit != 100 {
		t.Fatalf("after recovery: got %+v, want the shared limiter's decision", d)
	}
	if got := r.Degraded(); got != 4 {
		t.Errorf("Degraded() = %d, want 4", got)
	}
}

// A hung Redis must cost each request no more than the budget, and only until the
// breaker opens.
func TestResilientBudgetBoundsAHungRedis(t *testing.T) {
	r, shared, _ := newTestResilient(t, FallbackLocal)
	shared.hang.Store(true)

	for i := range 4 {
		start := time.Now()
		d, err := r.Allow(context.Background(), "a")
		took := time.Since(start)
		if err != nil || d.Limit != 5 {
			t.Fatalf("request %d: got %+v, %v; want the fallback", i+1, d, err)
		}
		if i < 3 && (took < 50*time.Millisecond || took > 500*time.Millisecond) {
			t.Errorf("request %d took %v, want about the 50ms budget", i+1, took)
		}
		if i == 3 && took > 10*time.Millisecond {
			t.Errorf("request 4, breaker open, took %v; want no wait", took)
		}
	}
}

func TestResilientFailModes(t *testing.T) {
	tests := []struct {
		mode      FailMode
		wantErr   error
		wantAllow bool
		wantLimit int
	}{
		{FallbackLocal, nil, true, 5},
		{FailOpen, nil, true, 0},
		{FailClosed, ErrUnavailable, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.mode.String(), func(t *testing.T) {
			r, shared, _ := newTestResilient(t, tc.mode)
			shared.down.Store(true)
			d, err := r.Allow(context.Background(), "a")
			if !errors.Is(err, tc.wantErr) || d.Allowed != tc.wantAllow || d.Limit != tc.wantLimit {
				t.Errorf("got %+v, %v; want allowed=%v limit=%d err=%v", d, err, tc.wantAllow, tc.wantLimit, tc.wantErr)
			}
		})
	}
}

// Requests from many goroutines while Redis flaps. Under -race this fails if the breaker
// or the counters are touched without synchronization.
func TestResilientConcurrentWhileFlapping(t *testing.T) {
	r, shared, clock := newTestResilient(t, FallbackLocal)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 500 {
				if _, err := r.Allow(context.Background(), "a"); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Go(func() {
		for i := range 200 {
			shared.down.Store(i%2 == 0)
			clock.Advance(time.Second)
		}
	})
	wg.Wait()
}

// With a real Redis client: the in-process Redis goes away and comes back on the same
// address. Requests never see an error, and shared limits resume after the cooldown.
func TestResilientRedisOutage(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1, DialerRetries: 1, ContextTimeoutEnabled: true})
	t.Cleanup(func() { rdb.Close() })

	shared, err := NewRedisTokenBucket(rdb, 10, 20)
	if err != nil {
		t.Fatal(err)
	}
	local, _ := newTestBucket(t, 1, 5)
	r, err := NewResilient(shared, local, FallbackLocal, 50*time.Millisecond, 2, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	limitNow := func() int {
		d, err := r.Allow(ctx, "a")
		if err != nil {
			t.Fatalf("request failed during the outage: %v", err)
		}
		return d.Limit
	}

	if got := limitNow(); got != 20 {
		t.Fatalf("Redis up: limit %d, want 20 (shared)", got)
	}
	mr.Close()
	for range 5 {
		if got := limitNow(); got != 5 {
			t.Fatalf("Redis down: limit %d, want 5 (local fallback)", got)
		}
	}
	if err := mr.Restart(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "shared limits to resume", func() bool { return limitNow() == 20 })
}
