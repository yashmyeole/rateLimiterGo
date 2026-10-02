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

	tests := []struct {
		name     string
		cfg      limiterConfig
		wantType string
		wantErr  bool
	}{
		{"off", limiterConfig{algorithm: "token-bucket", rate: 0}, "<nil>", false},
		{"token bucket in memory", limiterConfig{algorithm: "token-bucket", rate: 10, burst: 20}, "*limit.TokenBucket", false},
		{"fixed window in redis", limiterConfig{algorithm: "fixed-window", redisAddr: mr.Addr(), rate: 1, window: 10 * time.Second}, "*limit.FixedWindow", false},
		{"fixed window needs redis", limiterConfig{algorithm: "fixed-window", rate: 1, window: 10 * time.Second}, "<nil>", true},
		{"token bucket in redis not supported", limiterConfig{algorithm: "token-bucket", redisAddr: mr.Addr(), rate: 10, burst: 20}, "<nil>", true},
		{"unknown algorithm", limiterConfig{algorithm: "leaky-bucket", rate: 10, burst: 20}, "<nil>", true},
		{"redis unreachable", limiterConfig{algorithm: "fixed-window", redisAddr: "127.0.0.1:1", rate: 1, window: 10 * time.Second}, "<nil>", true},
		{"invalid burst", limiterConfig{algorithm: "token-bucket", rate: 10, burst: 0}, "<nil>", true},
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
