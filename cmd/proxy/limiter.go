package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/yashmyeole/ratelimiter-go/internal/limit"
)

// limiterConfig is what the rate-limit flags ask for.
type limiterConfig struct {
	algorithm    string        // "token-bucket", "sliding-window" or "fixed-window"
	redisAddr    string        // host:port; empty means in memory, token bucket only
	rate         float64       // requests per second per client; 0 turns limiting off
	burst        int           // token bucket only
	window       time.Duration // sliding and fixed window only
	redisTimeout time.Duration // time budget for each check in Redis
	fallback     string        // while Redis is unavailable: "local", "open" or "closed"
	replicas     int           // proxy replicas sharing the limit; the local fallback gets 1/replicas
}

const (
	breakerThreshold = 3               // Redis failures in a row that open the breaker
	breakerCooldown  = 2 * time.Second // how long it stays open before one trial request
)

var failModes = map[string]limit.FailMode{
	"local":  limit.FallbackLocal,
	"open":   limit.FailOpen,
	"closed": limit.FailClosed,
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

	mode, ok := failModes[cfg.fallback]
	if !ok {
		return nil, noop, fmt.Errorf("unknown -redis-fallback %q: want local, open or closed", cfg.fallback)
	}
	if cfg.replicas < 1 {
		return nil, noop, fmt.Errorf("-replicas must be at least 1, got %d", cfg.replicas)
	}

	// NewClient doesn't connect yet, so a bad flag fails below before any network call.
	rdb := redis.NewClient(&redis.Options{
		Addr: cfg.redisAddr,
		// Every check carries a deadline (the -redis-timeout budget). Without this option
		// go-redis ignores context deadlines for network reads and writes, and a hung
		// Redis would hold each request for its 3s read timeout instead.
		ContextTimeoutEnabled: true,
		// No retries: the circuit breaker decides what failures mean. Dials are retried
		// separately (5 attempts, 100ms apart, by default), which made every check against
		// a stopped Redis use its whole budget. DialerRetries counts attempts; 1 is a single
		// try, and 0 or less means the default.
		MaxRetries:    -1,
		DialerRetries: 1,
		DialTimeout:   time.Second,
	})

	var shared limit.Limiter
	var err error
	switch cfg.algorithm {
	case "token-bucket":
		shared, err = limit.NewRedisTokenBucket(rdb, cfg.rate, cfg.burst)
	case "sliding-window":
		shared, err = limit.NewSlidingWindow(rdb, cfg.rate, cfg.window)
	case "fixed-window":
		shared, err = limit.NewFixedWindow(rdb, cfg.rate, cfg.window)
	default:
		err = fmt.Errorf("unknown -algorithm %q: want token-bucket, sliding-window or fixed-window", cfg.algorithm)
	}
	if err != nil {
		rdb.Close()
		return nil, noop, err
	}

	// The local fallback is an in-memory token bucket with this replica's share of the
	// limit, so all replicas together still allow about the same total.
	var local limit.Limiter
	if mode == limit.FallbackLocal {
		burst := cfg.burst
		if cfg.algorithm != "token-bucket" {
			burst = int(math.Round(cfg.rate * cfg.window.Seconds())) // a window's worth
		}
		tb, err := limit.NewTokenBucket(cfg.rate/float64(cfg.replicas), max(1, burst/cfg.replicas))
		if err != nil {
			rdb.Close()
			return nil, noop, err
		}
		go tb.RunJanitor(ctx, janitorInterval)
		local = tb
	}

	r, err := limit.NewResilient(shared, local, mode, cfg.redisTimeout, breakerThreshold, breakerCooldown)
	if err != nil {
		rdb.Close()
		return nil, noop, err
	}

	// Starting while Redis is down is allowed: the proxy runs on the fallback until Redis
	// answers. Refusing to start would turn every Redis outage into a proxy outage at the
	// next deploy or restart.
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		slog.Warn("redis unreachable at startup, starting on the fallback", "addr", cfg.redisAddr, "err", err)
	}
	return r, func() { rdb.Close() }, nil
}
