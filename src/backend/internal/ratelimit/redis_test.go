package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestRedisLimiterAllowsWhenDisabled(t *testing.T) {
	limiter := NewRedisLimiter(nil, 0, time.Minute)
	allowed, err := limiter.Allow(context.Background(), "test")
	if err != nil {
		t.Fatalf("allow with disabled limiter: %v", err)
	}
	if !allowed {
		t.Fatal("disabled limiter should allow the request")
	}
}

func TestRedisLimiterAllowsWhenWindowIsInvalid(t *testing.T) {
	limiter := NewRedisLimiter(nil, 10, 0)
	allowed, err := limiter.Allow(context.Background(), "test")
	if err != nil {
		t.Fatalf("allow with invalid window: %v", err)
	}
	if !allowed {
		t.Fatal("invalid-window limiter should fail open for local configuration")
	}
}
