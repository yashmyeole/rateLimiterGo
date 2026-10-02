package limit

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

func size(tb *TokenBucket) int {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	return len(tb.buckets)
}

// waitFor polls cond until it's true, failing the test after 2s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestEvictIdleRemovesOnlyFullBuckets(t *testing.T) {
	tb, clock := newTestBucket(t, 1, 3) // 1 token/s, bursts of 3
	ctx := context.Background()

	for range 3 {
		tb.Allow(ctx, "drained") // 0 tokens left: full again in 3s
	}
	tb.Allow(ctx, "light") // 2 tokens left: full again in 1s

	clock.Advance(time.Second)
	if evicted, left := tb.evictIdle(); evicted != 1 || left != 1 {
		t.Fatalf("after 1s: evicted %d, left %d; want only the light user evicted", evicted, left)
	}
	if _, ok := tb.buckets["drained"]; !ok {
		t.Fatal("the drained client was evicted before its bucket refilled")
	}

	clock.Advance(2 * time.Second)
	if evicted, left := tb.evictIdle(); evicted != 1 || left != 0 {
		t.Errorf("after 3s: evicted %d, left %d; want the drained client evicted too", evicted, left)
	}
}

// Two limiters see the same random traffic on the same clock; one keeps every bucket,
// the other evicts idle ones at random moments. Every decision must match, which shows
// eviction only frees memory and never changes who gets a 429.
func TestEvictionNeverChangesDecisions(t *testing.T) {
	kept, clock := newTestBucket(t, 2, 3)
	evicting, err := NewTokenBucket(2, 3)
	if err != nil {
		t.Fatal(err)
	}
	evicting.now = clock.Now

	ctx := context.Background()
	rng := rand.New(rand.NewPCG(1, 2)) // fixed seed: the same traffic on every run
	var allowed, denied, evicted int

	for i := range 20_000 {
		clock.Advance(time.Duration(rng.N(150)) * time.Millisecond)
		if rng.N(50) == 0 {
			clock.Advance(3 * time.Second) // a quiet spell long enough for buckets to fill
		}
		if rng.N(10) == 0 {
			n, _ := evicting.evictIdle()
			evicted += n
		}

		key := fmt.Sprintf("client-%d", rng.N(8))
		want, _ := kept.Allow(ctx, key)
		got, _ := evicting.Allow(ctx, key)
		if got != want {
			t.Fatalf("step %d, %s: with eviction %+v, without %+v", i, key, got, want)
		}
		if got.Allowed {
			allowed++
		} else {
			denied++
		}
	}

	// Guard against a test that passes because it never exercised anything.
	if allowed == 0 || denied == 0 || evicted == 0 {
		t.Errorf("weak run: allowed %d, denied %d, evicted %d; want all > 0", allowed, denied, evicted)
	}
}

func TestEvictIdleRebuildsShrunkMap(t *testing.T) {
	tb, clock := newTestBucket(t, 1, 1)
	ctx := context.Background()

	for i := range 100 {
		tb.Allow(ctx, fmt.Sprintf("spike-%d", i)) // a spike of 100 clients
	}
	clock.Advance(time.Second) // they all refill and go idle
	tb.Allow(ctx, "steady")    // one client still active

	if evicted, left := tb.evictIdle(); evicted != 100 || left != 1 {
		t.Fatalf("evicted %d, left %d; want 100 and 1", evicted, left)
	}
	if tb.peak != 1 {
		t.Errorf("peak = %d after shrinking from 101 to 1, want 1: the map was not rebuilt", tb.peak)
	}
	if _, ok := tb.buckets["steady"]; !ok {
		t.Error("the active client's bucket was lost in the rebuild")
	}
}

func TestRunJanitorEvictsAndStops(t *testing.T) {
	tb, clock := newTestBucket(t, 10, 20)
	tb.Allow(context.Background(), "a")
	tb.Allow(context.Background(), "b")

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() { tb.RunJanitor(ctx, 10*time.Millisecond) })

	clock.Advance(time.Second) // both buckets refill completely
	waitFor(t, "idle buckets to be evicted", func() bool { return size(tb) == 0 })

	cancel()
	wg.Wait() // hangs, and the test times out, if RunJanitor ignores cancellation
}

// Requests and sweeps at the same time, as in the running proxy. Under -race this fails
// if evictIdle touches the map without the lock.
func TestJanitorConcurrentWithAllow(t *testing.T) {
	tb, err := NewTokenBucket(1000, 1) // real clock; buckets refill in 1ms, so sweeps evict constantly
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	var wg sync.WaitGroup
	for g := range 4 {
		wg.Go(func() {
			for i := range 2000 {
				tb.Allow(ctx, fmt.Sprintf("%d-%d", g, i%50))
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			tb.evictIdle()
		}
	})
	wg.Wait()
}
