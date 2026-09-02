package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

func newTestFileService() *FileService {
	return NewFileService(store.NewMemoryObjectStore(), store.NewMemoryFileStore())
}

func TestFileCheck(t *testing.T) {
	s := newTestFileService()
	ctx := context.Background()

	// 上传完整对象 "hash-a"（8 字节），使其真正存在
	initResp, err := s.InitUpload(ctx, "hash-a", 8)
	if err != nil || initResp.UploadID == "" {
		t.Fatalf("init: %+v err=%v", initResp, err)
	}
	e1, _ := s.UploadPart(ctx, "hash-a", initResp.UploadID, 1, []byte("1234"))
	e2, _ := s.UploadPart(ctx, "hash-a", initResp.UploadID, 2, []byte("5678"))
	if err := s.CompleteUpload(ctx, "hash-a", initResp.UploadID, 8, []store.UploadPartMeta{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	resp, err := s.CheckFiles(ctx, []model.FileCheckItem{
		{Hash: "hash-a", Size: 8}, // 完整 → 不缺失
		{Hash: "hash-b", Size: 5}, // 不存在 → 缺失
		{Hash: "hash-a", Size: 7}, // 大小与声明不符 → 缺失（损坏判定）
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Missing) != 2 {
		t.Fatalf("expected 2 missing (hash-b + size-mismatch), got %+v", resp.Missing)
	}
	// hash-b 与大小不符的 hash-a 都要出现
	missingHashes := map[string]bool{}
	for _, m := range resp.Missing {
		missingHashes[fmt.Sprintf("%s:%d", m.Hash, m.Size)] = true
	}
	if !missingHashes["hash-b:5"] || !missingHashes["hash-a:7"] {
		t.Fatalf("missing set wrong: %+v", resp.Missing)
	}
}

func TestFileUploadLifecycle(t *testing.T) {
	s := newTestFileService()
	ctx := context.Background()

	// init
	initResp, err := s.InitUpload(ctx, "hash-1", 8)
	if err != nil || initResp.Complete || initResp.UploadID == "" {
		t.Fatalf("bad init: %+v err=%v", initResp, err)
	}

	// 两个分片
	e1, err := s.UploadPart(ctx, "hash-1", initResp.UploadID, 1, []byte("1234"))
	if err != nil {
		t.Fatal(err)
	}
	e2, err := s.UploadPart(ctx, "hash-1", initResp.UploadID, 2, []byte("5678"))
	if err != nil {
		t.Fatal(err)
	}

	// complete
	err = s.CompleteUpload(ctx, "hash-1", initResp.UploadID, 8, []store.UploadPartMeta{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	})
	if err != nil {
		t.Fatalf("complete failed: %v", err)
	}

	// 文件已存在 → init 返回 complete=true（幂等）
	again, _ := s.InitUpload(ctx, "hash-1", 8)
	if !again.Complete {
		t.Fatal("expected complete=true for existing file")
	}
	// 上传已存在文件的分片 → ErrFileExists
	if _, err := s.UploadPart(ctx, "hash-1", "whatever", 1, []byte("x")); !errors.Is(err, ErrFileExists) {
		t.Fatalf("expected ErrFileExists, got %v", err)
	}
	// 预签名下载
	url, err := s.PresignDownload(ctx, "hash-1", "")
	if err != nil || url == "" {
		t.Fatalf("presign failed: %v", err)
	}
	// 不存在的文件 → 错误
	if _, err := s.PresignDownload(ctx, "hash-missing", ""); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestFileCompleteMissingPart(t *testing.T) {
	s := newTestFileService()
	ctx := context.Background()

	initResp, _ := s.InitUpload(ctx, "hash-2", 4)
	_, _ = s.UploadPart(ctx, "hash-2", initResp.UploadID, 1, []byte("1234"))

	err := s.CompleteUpload(ctx, "hash-2", initResp.UploadID, 4, []store.UploadPartMeta{
		{PartNumber: 1, ETag: "e1"},
		{PartNumber: 2, ETag: "e2"}, // part 2 未上传
	})
	if err == nil {
		t.Fatal("expected error for missing part")
	}
}
