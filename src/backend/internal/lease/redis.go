package lease

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var (
	ErrBusy        = errors.New("lease: busy")
	ErrUnavailable = errors.New("lease: unavailable")
)

type Handle interface {
	Release(ctx context.Context) error
}

type BatchLease interface {
	Acquire(ctx context.Context, batchID string) (Handle, error)
}

type RedisBatchLease struct {
	client         *redis.Client
	prefix         string
	ttl            time.Duration
	acquireTimeout time.Duration
	releaseScript  *redis.Script
}

func NewRedisBatchLease(
	client *redis.Client,
	ttl time.Duration,
	acquireTimeout time.Duration,
) *RedisBatchLease {
	return &RedisBatchLease{
		client:         client,
		prefix:         "ewaste:claim:lock:",
		ttl:            ttl,
		acquireTimeout: acquireTimeout,
		releaseScript: redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
	return redis.call('DEL', KEYS[1])
end
return 0
`),
	}
}

func (l *RedisBatchLease) Acquire(
	ctx context.Context,
	batchID string,
) (Handle, error) {
	if l.client == nil || l.ttl <= 0 || l.acquireTimeout <= 0 {
		return nil, ErrUnavailable
	}

	acquireCtx, cancel := context.WithTimeout(ctx, l.acquireTimeout)
	defer cancel()

	token := uuid.NewString()
	key := l.prefix + batchID

	err := l.client.SetArgs(
		acquireCtx,
		key,
		token,
		redis.SetArgs{
			Mode: "NX",
			TTL:  l.ttl,
		},
	).Err()

	if errors.Is(err, redis.Nil) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	return &redisLeaseHandle{
		client: l.client,
		key:    key,
		token:  token,
		script: l.releaseScript,
	}, nil
}

type redisLeaseHandle struct {
	client *redis.Client
	key    string
	token  string
	script *redis.Script
}

func (h *redisLeaseHandle) Release(ctx context.Context) error {
	if h == nil || h.client == nil {
		return nil
	}

	releaseCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()

	return h.script.Run(
		releaseCtx,
		h.client,
		[]string{h.key},
		h.token,
	).Err()
}
