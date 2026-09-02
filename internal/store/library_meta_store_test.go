package store

import (
	"testing"

	"TelebookServer/internal/model"
)

func TestComputeLibraryVersion_Deterministic(t *testing.T) {
	books := []LibraryBook{
		{UUID: "a", Name: "书A", Files: []model.BookFileMeta{
			{RelPath: "cover.jpg", Hash: "h1"}, {RelPath: "p1.jpg", Hash: "h2"},
		}},
		{UUID: "b", Name: "书B", Files: []model.BookFileMeta{{RelPath: "cover.jpg", Hash: "h3"}}},
	}
	v1 := ComputeLibraryVersion(books)
	// 书/文件顺序打乱，hash 应相同（确定性）
	shuffled := []LibraryBook{
		{UUID: "b", Name: "书B", Files: []model.BookFileMeta{{RelPath: "cover.jpg", Hash: "h3"}}},
		{UUID: "a", Name: "书A", Files: []model.BookFileMeta{
			{RelPath: "p1.jpg", Hash: "h2"}, {RelPath: "cover.jpg", Hash: "h1"},
		}},
	}
	v2 := ComputeLibraryVersion(shuffled)
	if v1 != v2 {
		t.Fatalf("version should be order-independent: %s vs %s", v1, v2)
	}
}

func TestComputeLibraryVersion_Changes(t *testing.T) {
	base := []LibraryBook{
		{UUID: "a", Name: "书A", Files: []model.BookFileMeta{{RelPath: "p.jpg", Hash: "h1"}}},
	}
	v0 := ComputeLibraryVersion(base)

	// 改名 → 变
	vName := ComputeLibraryVersion([]LibraryBook{
		{UUID: "a", Name: "书A改", Files: []model.BookFileMeta{{RelPath: "p.jpg", Hash: "h1"}}},
	})
	if vName == v0 {
		t.Fatal("rename should change version")
	}

	// 加文件 → 变
	vMore := ComputeLibraryVersion([]LibraryBook{
		{UUID: "a", Name: "书A", Files: []model.BookFileMeta{
			{RelPath: "p.jpg", Hash: "h1"}, {RelPath: "q.jpg", Hash: "h2"},
		}},
	})
	if vMore == v0 {
		t.Fatal("add file should change version")
	}

	// 文件 hash 变 → 变
	vHash := ComputeLibraryVersion([]LibraryBook{
		{UUID: "a", Name: "书A", Files: []model.BookFileMeta{{RelPath: "p.jpg", Hash: "hX"}}},
	})
	if vHash == v0 {
		t.Fatal("file hash change should change version")
	}

	// 空库 → 固定值
	empty := ComputeLibraryVersion(nil)
	if empty == "" {
		t.Fatal("empty library should have a deterministic hash")
	}
}

func TestComputeLibraryVersion_EmptyStable(t *testing.T) {
	if ComputeLibraryVersion(nil) != ComputeLibraryVersion(nil) {
		t.Fatal("empty library version should be stable")
	}
}
