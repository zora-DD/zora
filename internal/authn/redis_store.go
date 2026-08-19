package authn

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisSessionStore struct{ client redis.UniversalClient }

func NewRedisSessionStore(client redis.UniversalClient) *RedisSessionStore {
	if client == nil {
		return nil
	}
	return &RedisSessionStore{client: client}
}

func (s *RedisSessionStore) Put(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	return s.client.Set(ctx, key, value, ttl).Err()
}

func (s *RedisSessionStore) Take(ctx context.Context, key string) ([]byte, error) {
	value, err := s.client.GetDel(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, errSessionNotFound
	}
	return value, err
}

func (s *RedisSessionStore) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, errSessionNotFound
	}
	return value, err
}

func (s *RedisSessionStore) Refresh(ctx context.Context, key string, ttl time.Duration) error {
	return s.client.Expire(ctx, key, ttl).Err()
}

func (s *RedisSessionStore) Delete(ctx context.Context, key string) error {
	return s.client.Del(ctx, key).Err()
}
