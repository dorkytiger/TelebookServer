package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// MinioObjectStore ObjectStore 的 MinIO 实现（S3 协议）。
//
// 分片上传用 minio.Core 的低级 API（NewMultipartUpload / PutObjectPart /
// CompleteMultipartUpload），桶与预签名走 Core.Client。
//
// 预签名 URL 的签名绑定 host：客户端（手机）可达的地址与容器内不同
// （minio:9000 vs 192.168.31.202:19000）。因此预签名按调用方传入的
// host 动态创建 client 并缓存，host 为空时退回内部地址。
type MinioObjectStore struct {
	core          *minio.Core
	client        *minio.Client // 上传/操作用（内部 endpoint）
	bucket        string
	secure        bool
	accessKey     string
	secretKey     string
	publicPort    string        // 对外端口（host 自动推断时拼接用）
	publicHost    string        // 显式配置的完整公网地址 host:port（可选，覆盖自动推断）
	presignClient *minio.Client // 显式配置时的预签名 client
	presignMu     sync.Mutex
	presignCache  map[string]*minio.Client // host → client（自动推断缓存）
}

func NewMinioObjectStore(endpoint, accessKey, secretKey, bucket string, useSSL bool, publicPort, publicHost string) (*MinioObjectStore, error) {
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useSSL,
	}
	core, err := minio.NewCore(endpoint, opts)
	if err != nil {
		return nil, fmt.Errorf("init minio core: %w", err)
	}
	s := &MinioObjectStore{
		core:         core,
		client:       core.Client,
		bucket:       bucket,
		secure:       useSSL,
		accessKey:    accessKey,
		secretKey:    secretKey,
		publicPort:   publicPort,
		publicHost:   publicHost,
		presignCache: map[string]*minio.Client{},
	}
	if publicHost != "" {
		// 显式配置：预签名 URL 的签名绑定该 host，用其单独建 client
		if c, err := minio.New(publicHost, opts); err == nil {
			s.presignClient = c
		}
	}
	return s, nil
}

// presignClientFor 返回适合指定 host 的预签名 client：
// 显式配置优先；否则按 host 动态创建并缓存（同一 host 复用）。
func (s *MinioObjectStore) presignClientFor(host string) *minio.Client {
	if s.presignClient != nil {
		return s.presignClient
	}
	host = normalizeHost(host)
	if host == "" {
		return s.client // 未知 host：退回内部地址（容器内可达）
	}
	s.presignMu.Lock()
	defer s.presignMu.Unlock()
	if c, ok := s.presignCache[host]; ok {
		return c
	}
	opts := &minio.Options{
		Creds:  credentials.NewStaticV4(s.accessKey, s.secretKey, ""),
		Secure: s.secure,
	}
	endpoint := host
	if s.publicPort != "" {
		endpoint = net.JoinHostPort(host, s.publicPort)
	}
	if c, err := minio.New(endpoint, opts); err == nil {
		s.presignCache[host] = c
		return c
	}
	return s.client
}

// normalizeHost 去掉端口与空白，保留纯主机名（或 IP）。
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

func (s *MinioObjectStore) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !exists {
		return s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{})
	}
	return nil
}

func (s *MinioObjectStore) HasObject(ctx context.Context, key string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	var resp minio.ErrorResponse
	if errors.As(err, &resp) && resp.Code == "NoSuchKey" {
		return false, nil
	}
	return false, err
}

func (s *MinioObjectStore) InitUpload(ctx context.Context, key string) (string, error) {
	return s.core.NewMultipartUpload(ctx, s.bucket, key, minio.PutObjectOptions{})
}

func (s *MinioObjectStore) UploadPart(ctx context.Context, key, uploadID string, partNumber int, data []byte) (string, error) {
	part, err := s.core.PutObjectPart(ctx, s.bucket, key, uploadID, partNumber,
		bytes.NewReader(data), int64(len(data)), minio.PutObjectPartOptions{})
	if err != nil {
		return "", err
	}
	return part.ETag, nil
}

func (s *MinioObjectStore) CompleteUpload(ctx context.Context, key, uploadID string, parts []UploadPartMeta) error {
	complete := make([]minio.CompletePart, 0, len(parts))
	for _, p := range parts {
		complete = append(complete, minio.CompletePart{
			PartNumber: p.PartNumber,
			ETag:       p.ETag,
		})
	}
	_, err := s.core.CompleteMultipartUpload(ctx, s.bucket, key, uploadID, complete, minio.PutObjectOptions{})
	return err
}

func (s *MinioObjectStore) PresignDownload(ctx context.Context, key, host string) (string, error) {
	exists, err := s.HasObject(ctx, key)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", ErrObjectNotFound
	}
	client := s.presignClientFor(host)
	url, err := client.PresignedGetObject(ctx, s.bucket, key, 10*time.Minute, nil)
	if err != nil {
		return "", err
	}
	if url == nil {
		return "", errors.New("presign failed: nil url")
	}
	return url.String(), nil
}

// GetObject 打开对象内容（流式读取；供 API 代理下载，MinIO 无需公网端口）。
func (s *MinioObjectStore) GetObject(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, 0, err
	}
	// 立即探测一次，区分"对象不存在"与"打开成功"，并拿到大小
	info, err := obj.Stat()
	if err != nil {
		obj.Close()
		var resp minio.ErrorResponse
		if errors.As(err, &resp) && resp.Code == "NoSuchKey" {
			return nil, 0, ErrObjectNotFound
		}
		return nil, 0, err
	}
	return obj, info.Size, nil
}

// ObjectSize 返回对象实际大小（用于校验；不存在返回 ErrObjectNotFound）。
func (s *MinioObjectStore) ObjectSize(ctx context.Context, key string) (int64, error) {
	info, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		var resp minio.ErrorResponse
		if errors.As(err, &resp) && resp.Code == "NoSuchKey" {
			return 0, ErrObjectNotFound
		}
		return 0, err
	}
	return info.Size, nil
}

// 确保 ObjectStore 接口被满足。
var _ ObjectStore = (*MinioObjectStore)(nil)
