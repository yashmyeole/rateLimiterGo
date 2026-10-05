package limit

import (
	"context"
	_ "embed" // for the //go:embed Lua scripts below
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	//go:embed tokenbucket.lua
	tokenBucketLua string
	//go:embed slidingwindow.lua
	slidingWindowLua string

	// redis.Script sends EVALSHA (just the script's hash) and falls back to EVAL with
	// the full source if Redis doesn't have the script cached, e.g. after a restart.
	tokenBucketScript   = redis.NewScript(tokenBucketLua)
	slidingWindowScript = redis.NewScript(slidingWindowLua)
)

var (
	_ Limiter = (*RedisTokenBucket)(nil)
	_ Limiter = (*SlidingWindow)(nil)
)

// RedisTokenBucket is the same token bucket as TokenBucket, kept in Redis so every proxy
// replica shares it. The whole read-refill-spend-write runs as one Lua script, so it is
// atomic, and time comes from Redis rather than each proxy's clock.
type RedisTokenBucket struct {
	rdb   redis.Scripter
	rate  float64
	burst int
}

// NewRedisTokenBucket allows rate requests per second per key, with bursts of up to burst.
func NewRedisTokenBucket(rdb redis.Scripter, rate float64, burst int) (*RedisTokenBucket, error) {
	if rate <= 0 || burst < 1 {
		return nil, errors.New("token bucket needs rate > 0 and burst >= 1")
	}
	return &RedisTokenBucket{rdb: rdb, rate: rate, burst: burst}, nil
}

func (r *RedisTokenBucket) Allow(ctx context.Context, key string) (Decision, error) {
	res, err := tokenBucketScript.Run(ctx, r.rdb, []string{"rl:tb:{" + key + "}"}, r.rate, r.burst).Int64Slice()
	if err != nil {
		return Decision{}, fmt.Errorf("token bucket: %w", err)
	}
	return decision(res, r.burst), nil
}

// SlidingWindow allows about rate requests per second per key, measured over a window
// that slides with time instead of resetting all at once, so the fixed window's
// boundary burst can't happen. It runs as one Lua script in Redis, like RedisTokenBucket.
//
// It is an estimate: it assumes the previous window's requests were spread evenly.
type SlidingWindow struct {
	rdb    redis.Scripter
	limit  int
	window time.Duration
}

// NewSlidingWindow allows rate x window requests in any window-long span, e.g. rate 1
// with a 10s window allows 10 requests in any 10 seconds.
func NewSlidingWindow(rdb redis.Scripter, rate float64, window time.Duration) (*SlidingWindow, error) {
	limit := int(math.Round(rate * window.Seconds()))
	if window < time.Millisecond || limit < 1 {
		return nil, errors.New("sliding window needs a window of at least 1ms and rate x window >= 1 request")
	}
	return &SlidingWindow{rdb: rdb, limit: limit, window: window}, nil
}

func (s *SlidingWindow) Allow(ctx context.Context, key string) (Decision, error) {
	res, err := slidingWindowScript.Run(ctx, s.rdb, []string{"rl:sw:{" + key + "}"}, s.limit, s.window.Milliseconds()).Int64Slice()
	if err != nil {
		return Decision{}, fmt.Errorf("sliding window: %w", err)
	}
	return decision(res, s.limit), nil
}

// decision unpacks a script's {allowed, remaining, retry ms} reply.
func decision(res []int64, limit int) Decision {
	return Decision{
		Allowed:    res[0] == 1,
		Limit:      limit,
		Remaining:  int(res[1]),
		RetryAfter: time.Duration(res[2]) * time.Millisecond,
	}
}
