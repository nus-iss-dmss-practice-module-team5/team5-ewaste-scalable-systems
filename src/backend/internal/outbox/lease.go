package outbox

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type LeaderLeaseFactory interface {
	Acquire(context.Context, time.Duration) (LeaderLease, bool, error)
}

type LeaderLease interface {
	Renew(context.Context, time.Duration) error
	Release(context.Context) error
}

type RedisLeaderLeaseFactory struct {
	client redis.UniversalClient
	key    string
}

func NewRedisLeaderLeaseFactory(
	client redis.UniversalClient,
	key string,
) *RedisLeaderLeaseFactory {
	return &RedisLeaderLeaseFactory{client: client, key: key}
}

func (f *RedisLeaderLeaseFactory) Acquire(
	ctx context.Context,
	ttl time.Duration,
) (LeaderLease, bool, error) {
	if f == nil || f.client == nil {
		return nil, false, errors.New("outbox leader lease client is nil")
	}
	if ttl <= 0 {
		return nil, false, errors.New("outbox leader lease ttl must be positive")
	}

	token := uuid.NewString()
	acquired, err := f.client.SetNX(ctx, f.key, token, ttl).Result()
	if err != nil {
		return nil, false, err
	}
	if !acquired {
		return nil, false, nil
	}

	return &redisLeaderLease{
		client: f.client,
		key:    f.key,
		token:  token,
	}, true, nil
}

type redisLeaderLease struct {
	client redis.UniversalClient
	key    string
	token  string
}

var renewLeaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

var releaseLeaseScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
`)

func (l *redisLeaderLease) Renew(
	ctx context.Context,
	ttl time.Duration,
) error {
	result, err := renewLeaseScript.Run(
		ctx,
		l.client,
		[]string{l.key},
		l.token,
		ttl.Milliseconds(),
	).Int()
	if err != nil {
		return err
	}
	if result != 1 {
		return errors.New("outbox leader lease was lost")
	}
	return nil
}

func (l *redisLeaderLease) Release(ctx context.Context) error {
	_, err := releaseLeaseScript.Run(
		ctx,
		l.client,
		[]string{l.key},
		l.token,
	).Result()
	return err
}
