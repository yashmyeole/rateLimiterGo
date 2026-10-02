package limit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"
)

var _ Limiter = (*FixedWindow)(nil)

// FixedWindow counts each key's requests per window in Redis, for example 10 per
// 10 seconds, so every proxy replica shares the same counts.
//
// It has two known weaknesses, both documented in docs/decisions.md:
//   - At a window boundary a client can spend a full quota just before it and another
//     full quota just after it: twice the limit within moments.
//   - Windows are computed from each proxy's own clock, so replicas whose clocks
//     disagree also disagree on where a window starts.
type FixedWindow struct {
	rdb    *redis.Client
	limit  int
	window time.Duration
	now    func() time.Time // time.Now, swapped for a fake clock in tests
}

// NewFixedWindow allows rate requests per second on average, counted in windows of the
// given length: rate 1 with a 10s window means 10 requests per 10-second window.
func NewFixedWindow(rdb *redis.Client, rate float64, window time.Duration) (*FixedWindow, error) {
	limit := int(math.Round(rate * window.Seconds()))
	if window < time.Millisecond || limit < 1 {
		return nil, errors.New("fixed window needs a window of at least 1ms and rate x window >= 1 request")
	}
	return &FixedWindow{rdb: rdb, limit: limit, window: window, now: time.Now}, nil
}

// Allow counts one request for key in the current window.
func (fw *FixedWindow) Allow(ctx context.Context, key string) (Decision, error) {
	now := fw.now()
	w := fw.window.Milliseconds()
	start := time.UnixMilli(now.UnixMilli() / w * w) // windows line up with the Unix epoch
	redisKey := fmt.Sprintf("rl:fw:%s:%d", key, start.UnixMilli())

	// INCR and EXPIRE go in one MULTI/EXEC transaction. Sent as two separate commands, a
	// crash between them would leave a counter with no expiry, slowly filling Redis.
	var count *redis.IntCmd
	_, err := fw.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		count = pipe.Incr(ctx, redisKey)
		pipe.Expire(ctx, redisKey, fw.window)
		return nil
	})
	if err != nil {
		return Decision{}, fmt.Errorf("fixed window: %w", err)
	}

	n := int(count.Val())
	d := Decision{Limit: fw.limit, Remaining: max(0, fw.limit-n)}
	if n <= fw.limit {
		d.Allowed = true
		return d, nil
	}
	d.RetryAfter = start.Add(fw.window).Sub(now) // the next window starts fresh
	return d, nil
}
