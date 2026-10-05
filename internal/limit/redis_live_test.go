package limit

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// The tests in this file need a real Redis, whose network round trips are what leave
// room for races and what the benchmarks measure. They are skipped unless REDIS_ADDR is
// set:
//
//	make redis
//	REDIS_ADDR=127.0.0.1:6379 go test -run TestLostUpdate -v ./internal/limit
//	REDIS_ADDR=127.0.0.1:6379 go test -run '^$' -bench BenchmarkRedisAllow -cpu 1,10 ./internal/limit

func liveRedis(tb testing.TB, n int) []*redis.Client {
	tb.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		tb.Skip("set REDIS_ADDR to a real Redis to run, e.g. REDIS_ADDR=127.0.0.1:6379")
	}
	var clients []*redis.Client
	for range n {
		rdb := redis.NewClient(&redis.Options{Addr: addr})
		tb.Cleanup(func() { rdb.Close() })
		clients = append(clients, rdb)
	}
	if err := clients[0].Ping(context.Background()).Err(); err != nil {
		tb.Fatalf("redis at %s: %v", addr, err)
	}
	return clients
}

// racyTokenBucket is the token bucket done the naive way: read the bucket from Redis, do
// the math in Go, write it back. Between one request's read and its write another
// request can read the same old value, so two requests spend one token. It exists only
// to show that; RedisTokenBucket does the same work inside one Lua script.
type racyTokenBucket struct {
	rdb         *redis.Client
	rate, burst float64
}

func (r *racyTokenBucket) Allow(ctx context.Context, key string) (Decision, error) {
	key = "rl:racy:{" + key + "}"
	vals, err := r.rdb.HMGet(ctx, key, "tokens", "ts").Result()
	if err != nil {
		return Decision{}, err
	}

	now := time.Now()
	b := bucket{tokens: r.burst, last: now}
	if s, ok := vals[0].(string); ok {
		b.tokens, _ = strconv.ParseFloat(s, 64)
	}
	if s, ok := vals[1].(string); ok {
		ms, _ := strconv.ParseInt(s, 10, 64)
		b.last = time.UnixMilli(ms)
	}
	d := b.take(now, r.rate, r.burst)

	// Another request may have written since our read; this overwrites its update.
	return d, r.rdb.HSet(ctx, key, "tokens", b.tokens, "ts", now.UnixMilli()).Err()
}

// TestLostUpdate puts the same load on the naive limiter and the Lua one: a burst of 10,
// almost no refill, requests spread over two replicas. The first scenario has no
// overlap at all, so it checks the naive limiter's logic is right; any over-admission
// in the others comes from the race alone. The Lua version must allow exactly 10 in all.
func TestLostUpdate(t *testing.T) {
	clients := liveRedis(t, 2)
	ctx := context.Background()
	const rate, burst = 0.001, 10 // refill is negligible for the length of the test

	scenarios := []struct {
		name          string
		workers, each int // workers goroutines, each sending `each` requests one after another
	}{
		{"one at a time", 1, 200},
		{"10 workers x 20", 10, 20},
		{"200 at once", 200, 1},
	}
	run := func(limiters []Limiter, workers, each int) int {
		var allowed atomic.Int32
		var wg sync.WaitGroup
		for w := range workers {
			wg.Go(func() {
				for range each {
					if d, err := limiters[w%len(limiters)].Allow(ctx, "1.2.3.4"); err == nil && d.Allowed {
						allowed.Add(1)
					}
				}
			})
		}
		wg.Wait()
		return int(allowed.Load())
	}

	for _, sc := range scenarios {
		for round := 1; round <= 3; round++ {
			clients[0].Del(ctx, "rl:racy:{1.2.3.4}", "rl:tb:{1.2.3.4}")
			var racy, lua []Limiter
			for _, rdb := range clients {
				racy = append(racy, &racyTokenBucket{rdb: rdb, rate: rate, burst: burst})
				tb, err := NewRedisTokenBucket(rdb, rate, burst)
				if err != nil {
					t.Fatal(err)
				}
				lua = append(lua, tb)
			}

			racyAllowed := run(racy, sc.workers, sc.each)
			luaAllowed := run(lua, sc.workers, sc.each)
			t.Logf("%-16s round %d: read-then-write %3d of 200 allowed, Lua %2d of 200 (limit %d)",
				sc.name, round, racyAllowed, luaAllowed, burst)
			if luaAllowed != burst {
				t.Errorf("%s: the Lua script allowed %d, want exactly %d", sc.name, luaAllowed, burst)
			}
			if sc.workers == 1 && racyAllowed != burst {
				t.Errorf("%s: with no overlap the naive limiter allowed %d, want %d; its logic is wrong", sc.name, racyAllowed, burst)
			}
		}
	}
}

// BenchmarkRedisAllow measures one decision per algorithm against a real Redis, spread
// over 10,000 clients. With -cpu 1 the time per op is the round-trip latency; with more
// CPUs, requests overlap and it shows throughput.
func BenchmarkRedisAllow(b *testing.B) {
	rdb := liveRedis(b, 1)[0]
	keys := make([]string, 10_000)
	for i := range keys {
		keys[i] = fmt.Sprintf("bench-10.0.%d.%d", i/256, i%256)
	}

	tb, _ := NewRedisTokenBucket(rdb, 1e6, 1e6) // limits high enough that everything passes
	sw, _ := NewSlidingWindow(rdb, 1e6, 10*time.Second)
	fw, _ := NewFixedWindow(rdb, 1e6, 10*time.Second)
	limiters := []struct {
		name string
		l    Limiter
	}{{"token-bucket", tb}, {"sliding-window", sw}, {"fixed-window", fw}}

	for _, lim := range limiters {
		b.Run(lim.name, func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					if _, err := lim.l.Allow(context.Background(), keys[i%len(keys)]); err != nil {
						b.Error(err)
						return
					}
					i++
				}
			})
		})
	}
}
