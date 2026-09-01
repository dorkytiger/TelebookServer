package service

import (
	"context"
	"errors"
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

	// 登记一个已存在文件
	if err := s.files.UpsertMeta(ctx, "hash-a", 4, ""); err != nil {
		t.Fatal(err)
	}

	resp, err := s.CheckFiles(ctx, []model.FileCheckItem{
		{Hash: "hash-a", Size: 4},
		{Hash: "hash-b", Size: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Missing) != 1 || resp.Missing[0].Hash != "hash-b" {
		t.Fatalf("unexpected missing: %+v", resp.Missing)
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
