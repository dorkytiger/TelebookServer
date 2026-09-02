package service

import (
	"context"
	"testing"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

func TestAllFilesComplete(t *testing.T) {
	s := newTestFileService()
	ctx := context.Background()

	// 上传一个完整对象 "v-hash"（8 字节）
	initResp, _ := s.InitUpload(ctx, "v-hash", 8)
	e1, _ := s.UploadPart(ctx, "v-hash", initResp.UploadID, 1, []byte("1234"))
	e2, _ := s.UploadPart(ctx, "v-hash", initResp.UploadID, 2, []byte("5678"))
	if err := s.CompleteUpload(ctx, "v-hash", initResp.UploadID, 8, []store.UploadPartMeta{
		{PartNumber: 1, ETag: e1},
		{PartNumber: 2, ETag: e2},
	}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	cases := []struct {
		name  string
		files []model.BookFileMeta
		want  bool
	}{
		{"全部完整", []model.BookFileMeta{{Hash: "v-hash", Size: 8}}, true},
		{"缺失文件", []model.BookFileMeta{{Hash: "missing", Size: 8}}, false},
		{"大小不符(损坏)", []model.BookFileMeta{{Hash: "v-hash", Size: 9}}, false},
		{"混合", []model.BookFileMeta{{Hash: "v-hash", Size: 8}, {Hash: "missing", Size: 1}}, false},
	}
	for _, c := range cases {
		got, err := s.AllFilesComplete(ctx, c.files)
		if err != nil {
			t.Fatalf("%s: err %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
