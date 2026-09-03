package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"TelebookServer/internal/model"
)

// ErrUploadOrderLost 上传顺序缓存缺失（redis 崩/重启丢缓存）。
// complete 时客户端应重新 init（幂等重建任务 + 重写顺序缓存）后重试。
var ErrUploadOrderLost = errors.New("upload order cache lost, re-init required")

// UploadOrderStore 上传文件顺序缓存：
// init 时把客户端上报的文件清单顺序（= 书籍页序）缓存下来，
// complete 落库时按它构造 payload，避免从 book_upload_file 无序重建导致页序错乱。
type UploadOrderStore interface {
	// SaveOrder 保存某上传任务的文件顺序（init 成功时调用）。
	SaveOrder(ctx context.Context, uuid string, files []model.BookFileMeta) error
	// LoadOrder 读取顺序；缓存缺失返回 ErrUploadOrderLost。
	LoadOrder(ctx context.Context, uuid string) ([]model.BookFileMeta, error)
	// DeleteOrder 删除（任务终态/放弃时清理）。
	DeleteOrder(ctx context.Context, uuid string) error
}

const uploadOrderKeyPrefix = "telebook:upord:"
const uploadOrderTTL = 7 * 24 * time.Hour // 上传任务生命周期通常分钟级；断点续传跨天也够

// RedisUploadOrderStore Redis 实现：telebook:upord:<uuid> → JSON 文件清单（顺序即页序）。
type RedisUploadOrderStore struct {
	client *redis.Client
}

func NewRedisUploadOrderStore(addr string) (*RedisUploadOrderStore, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}
	return &RedisUploadOrderStore{client: client}, nil
}

func (s *RedisUploadOrderStore) SaveOrder(ctx context.Context, uuid string, files []model.BookFileMeta) error {
	b, err := json.Marshal(files)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, uploadOrderKeyPrefix+uuid, b, uploadOrderTTL).Err()
}

func (s *RedisUploadOrderStore) LoadOrder(ctx context.Context, uuid string) ([]model.BookFileMeta, error) {
	b, err := s.client.Get(ctx, uploadOrderKeyPrefix+uuid).Bytes()
	if err == redis.Nil {
		return nil, ErrUploadOrderLost
	}
	if err != nil {
		return nil, err
	}
	var files []model.BookFileMeta
	if err := json.Unmarshal(b, &files); err != nil {
		return nil, err
	}
	return files, nil
}

func (s *RedisUploadOrderStore) DeleteOrder(ctx context.Context, uuid string) error {
	return s.client.Del(ctx, uploadOrderKeyPrefix+uuid).Err()
}

// MemoryUploadOrderStore 内存实现（无 redis 部署 / 测试用）：重启即丢，
// complete 时缺失返回 ErrUploadOrderLost，客户端重新 init 即可（§8.2 幂等）。
type MemoryUploadOrderStore struct {
	mu    sync.Mutex
	order map[string][]model.BookFileMeta
}

func NewMemoryUploadOrderStore() *MemoryUploadOrderStore {
	return &MemoryUploadOrderStore{order: map[string][]model.BookFileMeta{}}
}

func (s *MemoryUploadOrderStore) SaveOrder(ctx context.Context, uuid string, files []model.BookFileMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]model.BookFileMeta, len(files))
	copy(cp, files)
	s.order[uuid] = cp
	return nil
}

func (s *MemoryUploadOrderStore) LoadOrder(ctx context.Context, uuid string) ([]model.BookFileMeta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files, ok := s.order[uuid]
	if !ok {
		return nil, ErrUploadOrderLost
	}
	cp := make([]model.BookFileMeta, len(files))
	copy(cp, files)
	return cp, nil
}

func (s *MemoryUploadOrderStore) DeleteOrder(ctx context.Context, uuid string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.order, uuid)
	return nil
}
