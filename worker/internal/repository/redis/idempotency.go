package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/Harsh-BH/Sentinel/worker/internal/repository"
)

var _ repository.IdempotencyStore = (*redisIdempotency)(nil)

const (
	lockKeyPrefix = "sentinel:lock:"
	lockTTL       = 10 * time.Minute
)

type redisIdempotency struct {
	client *goredis.Client
}

// NewRedisIdempotencyStore creates a Redis-backed idempotency store using ZADD NX.
func NewRedisIdempotencyStore(client *goredis.Client) repository.IdempotencyStore {
	return &redisIdempotency{client: client}
}

// AcquireLock uses Redis SETNX to atomically acquire a processing lock.
func (r *redisIdempotency) AcquireLock(ctx context.Context, jobID uuid.UUID) (bool, error) {
	key := lockKeyPrefix + jobID.String()
	ok, err := r.client.SetNX(ctx, key, time.Now().Unix(), lockTTL).Result()
	if err != nil {
		return false, fmt.Errorf("redis: acquire lock: %w", err)
	}
	return ok, nil
}

// ReleaseLock sets a TTL on the lock key for eventual cleanup.
func (r *redisIdempotency) ReleaseLock(ctx context.Context, jobID uuid.UUID) error {
	key := lockKeyPrefix + jobID.String()
	return r.client.Expire(ctx, key, lockTTL).Err()
}
