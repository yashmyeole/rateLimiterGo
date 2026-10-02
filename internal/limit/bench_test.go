package limit

import (
	"context"
	"fmt"
	"hash/maphash"
	"math/rand/v2"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Two alternatives to TokenBucket's single mutex, kept here only to measure against it.
// All three share the bucket math; they differ only in how a key finds its bucket.

// syncMapLimiter looks buckets up in a sync.Map (no lock to read an existing key) and
// locks each bucket separately.
type syncMapLimiter struct {
	rate, burst float64
	buckets     sync.Map // string -> *lockedBucket
}

type lockedBucket struct {
	mu sync.Mutex
	b  bucket
}

func (s *syncMapLimiter) allow(key string) Decision {
	now := time.Now()
	v, ok := s.buckets.Load(key)
	if !ok {
		v, _ = s.buckets.LoadOrStore(key, &lockedBucket{b: bucket{tokens: s.burst, last: now}})
	}
	lb := v.(*lockedBucket)
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return lb.b.take(now, s.rate, s.burst)
}

// shardedLimiter splits the map into 16 shards, each with its own mutex; a key's hash
// picks its shard, so requests from different clients rarely wait on the same lock.
type shardedLimiter struct {
	rate, burst float64
	seed        maphash.Seed
	shards      [16]shard
}

// shard is padded to 128 bytes, the cache line size on Apple M-series CPUs. Unpadded,
// 8 shards share a cache line and cores locking different shards still contend for
// it (false sharing): in our runs that made 10 CPUs slower than 4.
type shard struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	_       [128 - 16]byte
}

func newShardedLimiter(rate, burst float64) *shardedLimiter {
	s := &shardedLimiter{rate: rate, burst: burst, seed: maphash.MakeSeed()}
	for i := range s.shards {
		s.shards[i].buckets = make(map[string]*bucket)
	}
	return s
}

func (s *shardedLimiter) allow(key string) Decision {
	now := time.Now()
	sh := &s.shards[maphash.String(s.seed, key)%uint64(len(s.shards))]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	b, ok := sh.buckets[key]
	if !ok {
		b = &bucket{tokens: s.burst, last: now}
		sh.buckets[key] = b
	}
	return b.take(now, s.rate, s.burst)
}

// BenchmarkAllow runs each design under parallel load: every goroutine hammering one
// client's bucket, and goroutines spread over 10,000 clients. Run with -cpu 1,4,10 to
// see how each scales.
func BenchmarkAllow(b *testing.B) {
	keys := make([]string, 10_000)
	for i := range keys {
		keys[i] = fmt.Sprintf("10.0.%d.%d", i/256, i%256)
	}

	tb, err := NewTokenBucket(10, 20)
	if err != nil {
		b.Fatal(err)
	}
	designs := []struct {
		name  string
		allow func(key string) Decision
	}{
		{"mutex", func(k string) Decision { d, _ := tb.Allow(context.Background(), k); return d }},
		{"syncmap", (&syncMapLimiter{rate: 10, burst: 20}).allow},
		{"sharded16", newShardedLimiter(10, 20).allow},
	}

	for _, d := range designs {
		for _, k := range keys { // create every bucket first, so runs measure steady state
			d.allow(k)
		}
		b.Run(d.name+"/1-client", func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					d.allow(keys[0])
				}
			})
		})
		b.Run(d.name+"/10k-clients", func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				i := rand.N(len(keys)) // each goroutine starts somewhere different
				for pb.Next() {
					d.allow(keys[i])
					i = (i + 1) % len(keys)
				}
			})
		})
	}
}

// BenchmarkEvictIdleScan measures one janitor sweep over a map where nothing is idle
// yet: the time the lock is held, which every request waits through once per sweep.
func BenchmarkEvictIdleScan(b *testing.B) {
	for _, n := range []int{10_000, 100_000, 1_000_000} {
		b.Run(strconv.Itoa(n)+"-clients", func(b *testing.B) {
			tb, err := NewTokenBucket(10, 20)
			if err != nil {
				b.Fatal(err)
			}
			frozen := time.Now()
			tb.now = func() time.Time { return frozen } // no refill, so nothing gets evicted
			for i := range n {
				tb.Allow(context.Background(), strconv.Itoa(i))
			}
			for b.Loop() {
				tb.evictIdle()
			}
		})
	}
}
