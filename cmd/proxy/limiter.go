package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/yashmyeole/ratelimiter-go/internal/limit"
)

// limiterConfig is what the rate-limit flags ask for.
type limiterConfig struct {
	algorithm string        // "token-bucket", "sliding-window" or "fixed-window"
	redisAddr string        // host:port; empty means in memory, token bucket only
	rate      float64       // requests per second per client; 0 turns limiting off
	burst     int           // token bucket only
	window    time.Duration // sliding and fixed window only
}

// newLimiter builds the limiter cfg describes, or returns nil if rate limiting is off.
// Background work (the token bucket's janitor) runs until ctx is canceled. The returned
// function closes the Redis connection pool, if one was opened.
func newLimiter(ctx context.Context, cfg limiterConfig) (limit.Limiter, func(), error) {
	noop := func() {}
	if cfg.rate <= 0 {
		return nil, noop, nil
	}

	if cfg.redisAddr == "" {
		if cfg.algorithm != "token-bucket" {
			return nil, noop, fmt.Errorf("-algorithm %s needs -redis; only token-bucket runs in memory", cfg.algorithm)
		}
		tb, err := limit.NewTokenBucket(cfg.rate, cfg.burst)
		if err != nil {
			return nil, noop, err
		}
		go tb.RunJanitor(ctx, janitorInterval)
		return tb, noop, nil
	}

	// NewClient doesn't connect yet, so a bad flag fails below before any network call.
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.redisAddr,
		// go-redis waits seconds by default; a rate check shouldn't hold a request
		// that long when Redis is slow or gone.
		DialTimeout:  time.Second,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
	})

	var l limit.Limiter
	var err error
	switch cfg.algorithm {
	case "token-bucket":
		l, err = limit.NewRedisTokenBucket(rdb, cfg.rate, cfg.burst)
	case "sliding-window":
		l, err = limit.NewSlidingWindow(rdb, cfg.rate, cfg.window)
	case "fixed-window":
		l, err = limit.NewFixedWindow(rdb, cfg.rate, cfg.window)
	default:
		err = fmt.Errorf("unknown -algorithm %q: want token-bucket, sliding-window or fixed-window", cfg.algorithm)
	}
	if err != nil {
		rdb.Close()
		return nil, noop, err
	}

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		rdb.Close()
		return nil, noop, fmt.Errorf("redis at %s: %w", cfg.redisAddr, err)
	}
	return l, func() { rdb.Close() }, nil
}
