package service

import (
	"context"
	"errors"
	"fmt"
	"io"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

var (
	// ErrFileExists 文件已存在（幂等完成）。
	ErrFileExists = errors.New("file already exists")
	// ErrUploadNotComplete 分片未齐全。
	ErrUploadNotComplete = errors.New("upload parts incomplete")
)

// FileService 文件同步：指纹比对 / 分片上传（断点续传）/ 预签名下载。
//
// 对象键 = "files/<sha256>"（内容寻址 → 跨设备去重）。
type FileService struct {
	objects store.ObjectStore
	files   store.FileStore
	bucket  string // 实际使用存储桶前缀由 ObjectStore 持有；此处仅用于构造键
}

func NewFileService(objects store.ObjectStore, files store.FileStore) *FileService {
	return &FileService{objects: objects, files: files}
}

func objectKey(hash string) string { return "files/" + hash }

// CheckFiles 批量比对：返回远端缺失清单。
func (s *FileService) CheckFiles(ctx context.Context, items []model.FileCheckItem) (*model.FileCheckResponse, error) {
	hashes := make([]string, 0, len(items))
	for _, it := range items {
		hashes = append(hashes, it.Hash)
	}
	found, err := s.files.HasHashes(ctx, hashes)
	if err != nil {
		return nil, err
	}
	return &model.FileCheckResponse{Missing: store.MissingHashes(items, found)}, nil
}

// InitUpload 初始化分片上传。文件已存在时返回 complete=true（幂等）。
func (s *FileService) InitUpload(ctx context.Context, hash string, size int64) (*model.FileInitUploadResponse, error) {
	if size < 0 {
		return nil, errors.New("invalid size")
	}
	exists, err := s.objects.HasObject(ctx, objectKey(hash))
	if err != nil {
		return nil, err
	}
	if exists {
		return &model.FileInitUploadResponse{Complete: true}, nil
	}
	uploadID, err := s.objects.InitUpload(ctx, objectKey(hash))
	if err != nil {
		return nil, err
	}
	return &model.FileInitUploadResponse{UploadID: uploadID}, nil
}

// UploadPart 上传一个分片，返回该分片 ETag。
// 若文件已存在（另一设备已传完），返回 ErrFileExists 表示可直接跳过。
func (s *FileService) UploadPart(ctx context.Context, hash, uploadID string, partNumber int, data []byte) (string, error) {
	if partNumber < 1 || len(data) == 0 {
		return "", errors.New("invalid part")
	}
	exists, err := s.objects.HasObject(ctx, objectKey(hash))
	if err != nil {
		return "", err
	}
	if exists {
		return "", ErrFileExists
	}
	return s.objects.UploadPart(ctx, objectKey(hash), uploadID, partNumber, data)
}

// CompleteUpload 完成分片上传并登记 file_meta。
// parts 由客户端按上传顺序提供（partNumber + 各分片 ETag）。
func (s *FileService) CompleteUpload(ctx context.Context, hash, uploadID string, size int64, parts []store.UploadPartMeta) error {
	if len(parts) == 0 {
		return ErrUploadNotComplete
	}
	if err := s.objects.CompleteUpload(ctx, objectKey(hash), uploadID, parts); err != nil {
		return fmt.Errorf("complete upload: %w", err)
	}
	return s.files.UpsertMeta(ctx, hash, size, "")
}

// PresignDownload 生成下载地址（302 重定向目标）。
// host 为客户端可达的主机名（从请求推断）；空则用存储内部地址。
func (s *FileService) PresignDownload(ctx context.Context, hash, host string) (string, error) {
	return s.objects.PresignDownload(ctx, objectKey(hash), host)
}

// Download 打开文件内容流（API 代理下载：MinIO 无需公网端口）。
// 调用方负责 Close 返回的流；文件不存在返回 ErrObjectNotFound。
func (s *FileService) Download(ctx context.Context, hash string) (io.ReadCloser, error) {
	rc, err := s.objects.GetObject(ctx, objectKey(hash))
	if err != nil {
		return nil, err
	}
	return rc, nil
}
