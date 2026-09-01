package store

import (
	"context"
	"errors"
	"time"
)

// ErrRefreshTokenNotFound refresh token 不存在/已过期/已吊销。
var ErrRefreshTokenNotFound = errors.New("refresh token not found")

// TokenStore refresh token 存储（Redis 实现；未配置 Redis 时用内存实现）。
//
// 只存 refresh token 的 sha256 哈希 → device_id，天然支持：
//   - TTL 自动过期
//   - 轮换 = 删旧 key + 写新 key
//   - 吊销 = 删 key
type TokenStore interface {
	SaveRefreshToken(ctx context.Context, tokenHash, deviceID string, ttl time.Duration) error
	GetDeviceByRefreshToken(ctx context.Context, tokenHash string) (string, error)
	DeleteRefreshToken(ctx context.Context, tokenHash string) error
}
