package ratelimit

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type Limiter interface {
	Allow(ctx context.Context, key string) (bool, error)
}

type RedisLimiter struct {
	client *redis.Client
	limit  int
	window time.Duration
	prefix string
	script *redis.Script
}

func NewRedisLimiter(client *redis.Client, limit int, window time.Duration) *RedisLimiter {
	return &RedisLimiter{
		client: client, limit: limit, window: window, prefix: "ewaste:rate:",
		script: redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`),
	}
}

func (l *RedisLimiter) Allow(ctx context.Context, key string) (bool, error) {
	if l.limit <= 0 || l.window <= 0 {
		return true, nil
	}
	count, err := l.script.Run(ctx, l.client, []string{l.prefix + key}, l.window.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return count <= int64(l.limit), nil
}
