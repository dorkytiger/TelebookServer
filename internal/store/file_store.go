package store

import (
	"context"
	"fmt"
	"sync"

	"TelebookServer/internal/model"
)

// FileStore 文件元数据存储（MinIO 对象引用登记）。
type FileStore interface {
	// UpsertMeta 登记文件（已存在则幂等忽略）。
	UpsertMeta(ctx context.Context, hash string, size int64, mimeType string) error
	// HasHashes 返回已存在（在 file_meta 中）的 hash 集合。
	HasHashes(ctx context.Context, hashes []string) (map[string]bool, error)
}

// PGFileStore PostgreSQL 实现。
type PGFileStore struct {
	pool Pool
}

func NewPGFileStore(pool Pool) *PGFileStore {
	return &PGFileStore{pool: pool}
}

func (s *PGFileStore) UpsertMeta(ctx context.Context, hash string, size int64, mimeType string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO file_meta (hash, size, mime_type)
		VALUES ($1, $2, $3)
		ON CONFLICT (hash) DO UPDATE SET last_used_at = now()`,
		hash, size, mimeType,
	)
	return err
}

func (s *PGFileStore) HasHashes(ctx context.Context, hashes []string) (map[string]bool, error) {
	if len(hashes) == 0 {
		return map[string]bool{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT hash FROM file_meta WHERE hash = ANY($1)`, hashes)
	if err != nil {
		return nil, fmt.Errorf("query file_meta: %w", err)
	}
	defer rows.Close()

	found := make(map[string]bool, len(hashes))
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		found[h] = true
	}
	return found, rows.Err()
}

// FileCheckResult 便捷封装：给定清单，返回缺失项。
func MissingHashes(all []model.FileCheckItem, found map[string]bool) []model.FileCheckItem {
	missing := make([]model.FileCheckItem, 0, len(all))
	for _, item := range all {
		if !found[item.Hash] {
			missing = append(missing, item)
		}
	}
	return missing
}

// MemoryFileStore 内存版文件元数据（测试用）。
type MemoryFileStore struct {
	mu    sync.Mutex
	files map[string]int64 // hash → size
}

func NewMemoryFileStore() *MemoryFileStore {
	return &MemoryFileStore{files: map[string]int64{}}
}

var _ FileStore = (*MemoryFileStore)(nil)

func (s *MemoryFileStore) UpsertMeta(_ context.Context, hash string, size int64, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[hash]; !ok {
		s.files[hash] = size
	}
	return nil
}

func (s *MemoryFileStore) HasHashes(_ context.Context, hashes []string) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		if _, ok := s.files[h]; ok {
			found[h] = true
		}
	}
	return found, nil
}
