package store

import (
	"context"
	"sync"
	"time"
)

// MemoryTokenStore 内存实现（测试 / 未配置 Redis 的本地开发）。
type MemoryTokenStore struct {
	mu     sync.Mutex
	tokens map[string]memoryRefreshToken
}

type memoryRefreshToken struct {
	deviceID string
	expires  time.Time
}

func NewMemoryTokenStore() *MemoryTokenStore {
	return &MemoryTokenStore{tokens: map[string]memoryRefreshToken{}}
}

func (s *MemoryTokenStore) SaveRefreshToken(_ context.Context, tokenHash, deviceID string, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[tokenHash] = memoryRefreshToken{deviceID: deviceID, expires: time.Now().Add(ttl)}
	return nil
}

func (s *MemoryTokenStore) GetDeviceByRefreshToken(_ context.Context, tokenHash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[tokenHash]
	if !ok || time.Now().After(t.expires) {
		return "", ErrRefreshTokenNotFound
	}
	return t.deviceID, nil
}

func (s *MemoryTokenStore) DeleteRefreshToken(_ context.Context, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, tokenHash)
	return nil
}
