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
	algorithm string        // "token-bucket" (in memory) or "fixed-window" (in Redis)
	redisAddr string        // host:port; empty means no Redis
	rate      float64       // requests per second per client; 0 turns limiting off
	burst     int           // token bucket only
	window    time.Duration // fixed window only
}

// newLimiter builds the limiter cfg describes, or returns nil if rate limiting is off.
// Background work (the token bucket's janitor) runs until ctx is canceled. The returned
// function closes the Redis connection pool, if one was opened.
func newLimiter(ctx context.Context, cfg limiterConfig) (limit.Limiter, func(), error) {
	noop := func() {}
	if cfg.rate <= 0 {
		return nil, noop, nil
	}

	switch {
	case cfg.algorithm == "token-bucket" && cfg.redisAddr == "":
		tb, err := limit.NewTokenBucket(cfg.rate, cfg.burst)
		if err != nil {
			return nil, noop, err
		}
		go tb.RunJanitor(ctx, janitorInterval)
		return tb, noop, nil

	case cfg.algorithm == "fixed-window" && cfg.redisAddr != "":
		rdb := redis.NewClient(&redis.Options{
			Addr: cfg.redisAddr,
			// go-redis waits seconds by default; a rate check shouldn't hold a request
			// that long when Redis is slow or gone.
			DialTimeout:  time.Second,
			ReadTimeout:  500 * time.Millisecond,
			WriteTimeout: 500 * time.Millisecond,
		})
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := rdb.Ping(pingCtx).Err(); err != nil {
			rdb.Close()
			return nil, noop, fmt.Errorf("redis at %s: %w", cfg.redisAddr, err)
		}
		fw, err := limit.NewFixedWindow(rdb, cfg.rate, cfg.window)
		if err != nil {
			rdb.Close()
			return nil, noop, err
		}
		return fw, func() { rdb.Close() }, nil

	default:
		return nil, noop, fmt.Errorf("-algorithm %q with -redis %q: use token-bucket without -redis, or fixed-window with -redis",
			cfg.algorithm, cfg.redisAddr)
	}
}
