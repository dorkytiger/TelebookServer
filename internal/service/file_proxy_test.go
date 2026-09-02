package service

import (
	"context"
	"errors"
	"io"
	"testing"

	"TelebookServer/internal/store"
)

func TestFileProxyDownload(t *testing.T) {
	s := newTestFileService()
	ctx := context.Background()

	// 上传一个文件（分片 → 完成 → 登记）
	initResp, err := s.InitUpload(ctx, "proxy-hash", 8)
	if err != nil || initResp.UploadID == "" {
		t.Fatalf("init failed: %v", err)
	}
	e1, _ := s.UploadPart(ctx, "proxy-hash", initResp.UploadID, 1, []byte("hello"))
	e2, _ := s.UploadPart(ctx, "proxy-hash", initResp.UploadID, 2, []byte("world"))
	if err := s.CompleteUpload(ctx, "proxy-hash", initResp.UploadID, 10, []store.UploadPartMeta{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	}); err != nil {
		t.Fatalf("complete failed: %v", err)
	}

	// 代理下载：流式读回内容
	rc, size, err := s.Download(ctx, "proxy-hash")
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if size != int64(len(data)) {
		t.Fatalf("size mismatch: got %d want %d", size, len(data))
	}
	if string(data) != "helloworld" {
		t.Fatalf("content mismatch: %q", string(data))
	}

	// 不存在的文件 → ErrObjectNotFound
	if _, _, err := s.Download(ctx, "no-such-hash"); !errors.Is(err, store.ErrObjectNotFound) {
		t.Fatalf("expected ErrObjectNotFound, got %v", err)
	}
}
