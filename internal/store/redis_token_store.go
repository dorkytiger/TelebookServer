package store

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

const refreshTokenKeyPrefix = "rt:"

// RedisTokenStore Redis 实现：rt:<sha256> → device_id（TTL = refresh token 有效期）。
type RedisTokenStore struct {
	client *redis.Client
}

func NewRedisTokenStore(addr string) (*RedisTokenStore, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}
	return &RedisTokenStore{client: client}, nil
}

func (s *RedisTokenStore) SaveRefreshToken(ctx context.Context, tokenHash, deviceID string, ttl time.Duration) error {
	return s.client.Set(ctx, refreshTokenKeyPrefix+tokenHash, deviceID, ttl).Err()
}

func (s *RedisTokenStore) GetDeviceByRefreshToken(ctx context.Context, tokenHash string) (string, error) {
	deviceID, err := s.client.Get(ctx, refreshTokenKeyPrefix+tokenHash).Result()
	if err == redis.Nil {
		return "", ErrRefreshTokenNotFound
	}
	return deviceID, err
}

func (s *RedisTokenStore) DeleteRefreshToken(ctx context.Context, tokenHash string) error {
	return s.client.Del(ctx, refreshTokenKeyPrefix+tokenHash).Err()
}
