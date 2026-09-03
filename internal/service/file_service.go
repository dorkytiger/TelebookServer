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

// AllFilesComplete 校验文件列表是否全部在对象存储完整存在（实际大小 == 声明大小）。
// 实现 FileVerifier：任一文件缺失/大小不符 → false。
func (s *FileService) AllFilesComplete(ctx context.Context, files []model.BookFileMeta) (bool, error) {
	for _, f := range files {
		if f.Hash == "" {
			continue
		}
		size, err := s.objects.ObjectSize(ctx, objectKey(f.Hash))
		if err != nil {
			if errors.Is(err, store.ErrObjectNotFound) {
				return false, nil // 缺失
			}
			return false, err
		}
		if size != f.Size {
			// 实际大小与声明不符：对象不完整/损坏
			return false, nil
		}
	}
	return true, nil
}

// objectKey 返回对象键前缀。
func objectKey(hash string) string { return "files/" + hash }

// CheckFiles 批量比对：返回远端缺失清单。
//
// 存在性基于 MinIO **实际对象大小**与客户端声明的 size 对比：
// 对象不存在、或实际大小与声明不符（上传不完整/曾损坏），一律判为缺失，
// 驱使客户端重新上传（内容寻址：重新上传会用真实文件大小）。
func (s *FileService) CheckFiles(ctx context.Context, items []model.FileCheckItem) (*model.FileCheckResponse, error) {
	missing := make([]model.FileCheckItem, 0, len(items))
	for _, it := range items {
		size, err := s.objects.ObjectSize(ctx, objectKey(it.Hash))
		if err != nil {
			if errors.Is(err, store.ErrObjectNotFound) {
				missing = append(missing, it)
				continue
			}
			// 存储查询异常：保守起见视为缺失（客户端会尝试重传）
			missing = append(missing, it)
			continue
		}
		if size != it.Size {
			// 实际大小与声明不符：对象损坏/不完整 → 判缺失，触发重传
			missing = append(missing, it)
		}
	}
	return &model.FileCheckResponse{Missing: missing}, nil
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

// DirectUpload 整文件直传（iOS 后台 URLSession uploadTask / MB 级图片直传）：
// 对象已存在且大小一致 → 幂等跳过；否则整文件单 PUT 写入并登记 file_meta。
func (s *FileService) DirectUpload(ctx context.Context, hash string, size int64, reader io.Reader) error {
	if hash == "" || size < 0 {
		return errors.New("invalid direct upload params")
	}
	key := objectKey(hash)
	// 幂等：对象已存在且大小一致（另一设备/上次已传完）→ 跳过
	if sz, err := s.objects.ObjectSize(ctx, key); err == nil && sz == size {
		return nil
	}
	if err := s.objects.PutObject(ctx, key, size, reader); err != nil {
		return fmt.Errorf("direct upload: %w", err)
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
func (s *FileService) Download(ctx context.Context, hash string) (io.ReadCloser, int64, error) {
	return s.objects.GetObject(ctx, objectKey(hash))
}
