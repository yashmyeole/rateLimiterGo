package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestNewLimiter(t *testing.T) {
	mr := miniredis.RunT(t)
	redisCfg := func(algorithm, fallback string) limiterConfig {
		return limiterConfig{
			algorithm: algorithm, redisAddr: mr.Addr(), rate: 1, burst: 10, window: 10 * time.Second,
			redisTimeout: 50 * time.Millisecond, fallback: fallback, replicas: 2,
		}
	}

	tests := []struct {
		name     string
		cfg      limiterConfig
		wantType string
		wantErr  bool
	}{
		{"off", limiterConfig{algorithm: "token-bucket", rate: 0}, "<nil>", false},
		{"token bucket in memory", limiterConfig{algorithm: "token-bucket", rate: 10, burst: 20}, "*limit.TokenBucket", false},
		{"token bucket in redis", redisCfg("token-bucket", "local"), "*limit.Resilient", false},
		{"sliding window in redis", redisCfg("sliding-window", "local"), "*limit.Resilient", false},
		{"fixed window in redis", redisCfg("fixed-window", "local"), "*limit.Resilient", false},
		{"fail open", redisCfg("token-bucket", "open"), "*limit.Resilient", false},
		{"fail closed", redisCfg("token-bucket", "closed"), "*limit.Resilient", false},
		{"sliding window needs redis", limiterConfig{algorithm: "sliding-window", rate: 1, window: 10 * time.Second}, "<nil>", true},
		{"fixed window needs redis", limiterConfig{algorithm: "fixed-window", rate: 1, window: 10 * time.Second}, "<nil>", true},
		{"unknown algorithm in memory", limiterConfig{algorithm: "leaky-bucket", rate: 10, burst: 20}, "<nil>", true},
		{"unknown algorithm in redis", redisCfg("leaky-bucket", "local"), "<nil>", true},
		{"unknown fallback", redisCfg("token-bucket", "maybe"), "<nil>", true},
		{"zero replicas", func() limiterConfig { c := redisCfg("token-bucket", "local"); c.replicas = 0; return c }(), "<nil>", true},
		{"invalid burst, rejected before connecting", func() limiterConfig {
			c := redisCfg("token-bucket", "local")
			c.redisAddr = "127.0.0.1:1"
			c.burst = 0
			return c
		}(), "<nil>", true},
		{"redis unreachable: starts on the fallback", func() limiterConfig { c := redisCfg("sliding-window", "local"); c.redisAddr = "127.0.0.1:1"; return c }(), "*limit.Resilient", false},
		{"invalid burst in memory", limiterConfig{algorithm: "token-bucket", rate: 10, burst: 0}, "<nil>", true},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the janitor of any token bucket started below

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l, closeLimiter, err := newLimiter(ctx, tc.cfg)
			defer closeLimiter()
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got := fmt.Sprintf("%T", l); got != tc.wantType {
				t.Errorf("limiter type = %s, want %s", got, tc.wantType)
			}
		})
	}
}
