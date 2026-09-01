package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// UploadPartMeta 已上传分片的信息（S3 完成上传需要 ETag）。
type UploadPartMeta struct {
	PartNumber int
	ETag       string
}

// ErrObjectNotFound 对象不存在。
var ErrObjectNotFound = errors.New("object not found")

// ObjectStore 对象存储抽象（MinIO 实现 / 内存实现）。
// 对象键由调用方构造（如 "files/<hash>"）。
type ObjectStore interface {
	// EnsureBucket 确保存储桶存在。
	EnsureBucket(ctx context.Context) error
	// HasObject 对象是否已存在。
	HasObject(ctx context.Context, key string) (bool, error)
	// InitUpload 初始化分片上传，返回 upload_id。
	InitUpload(ctx context.Context, key string) (string, error)
	// UploadPart 上传一个分片（partNumber 从 1 开始），返回该分片 ETag。
	UploadPart(ctx context.Context, key, uploadID string, partNumber int, data []byte) (string, error)
	// CompleteUpload 完成分片上传，组装为最终对象。
	CompleteUpload(ctx context.Context, key, uploadID string, parts []UploadPartMeta) error
	// PresignDownload 生成可下载的预签名 URL。
	// host 为客户端可达的主机名（如 "192.168.31.202"）；空则用存储内部地址。
	PresignDownload(ctx context.Context, key, host string) (string, error)
}

// MemoryObjectStore 内存对象存储（测试/本地调试用）。
type MemoryObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte         // 最终对象
	uploads map[string]map[int][]byte // uploadID → partNumber → 数据
	seq     int
}

func NewMemoryObjectStore() *MemoryObjectStore {
	return &MemoryObjectStore{
		objects: map[string][]byte{},
		uploads: map[string]map[int][]byte{},
	}
}

var _ ObjectStore = (*MemoryObjectStore)(nil)

func (s *MemoryObjectStore) EnsureBucket(_ context.Context) error { return nil }

func (s *MemoryObjectStore) HasObject(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok, nil
}

func (s *MemoryObjectStore) InitUpload(_ context.Context, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	id := fmt.Sprintf("mem-upload-%d", s.seq)
	s.uploads[id] = map[int][]byte{}
	return id, nil
}

func (s *MemoryObjectStore) UploadPart(_ context.Context, _, uploadID string, partNumber int, data []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts, ok := s.uploads[uploadID]
	if !ok {
		return "", errors.New("unknown upload_id")
	}
	parts[partNumber] = data
	return fmt.Sprintf("etag-%d", partNumber), nil
}

func (s *MemoryObjectStore) CompleteUpload(_ context.Context, key, uploadID string, parts []UploadPartMeta) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	partData, ok := s.uploads[uploadID]
	if !ok {
		return errors.New("unknown upload_id")
	}
	var buf []byte
	for _, p := range parts {
		data, ok := partData[p.PartNumber]
		if !ok {
			return fmt.Errorf("missing part %d", p.PartNumber)
		}
		buf = append(buf, data...)
	}
	s.objects[key] = buf
	delete(s.uploads, uploadID)
	return nil
}

func (s *MemoryObjectStore) PresignDownload(_ context.Context, key, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.objects[key]; !ok {
		return "", ErrObjectNotFound
	}
	return "memory://" + key, nil
}
