package service

import (
	"context"
	"encoding/json"
	"testing"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

// 内存库版本存储（测试用）。
type memLibVersion struct{ v string }

func (m *memLibVersion) GetBookVersion(ctx context.Context) (string, error) { return m.v, nil }
func (m *memLibVersion) SetBookVersion(ctx context.Context, v string) error { m.v = v; return nil }

func TestRefreshLibraryVersion(t *testing.T) {
	mem := store.NewMemorySyncStore()
	s := NewSyncService(mem, mem, mem, mem, mem, mem)
	lib := &memLibVersion{}
	s.SetLibraryStore(lib)
	ctx := context.Background()

	// 初始空库 → 稳定版本
	if err := s.RefreshLibraryVersion(ctx); err != nil {
		t.Fatal(err)
	}
	v0 := lib.v
	if v0 == "" {
		t.Fatal("empty library should have a version")
	}

	// 模拟加一本书（写 current_book via ApplyBookChange）
	payload := model.BookPayload{
		Name: "书A",
		Files: []model.BookFileMeta{
			{RelPath: "cover.jpg", Hash: "h1"},
			{RelPath: "p1.jpg", Hash: "h2"},
		},
	}
	rawPayload, _ := json.Marshal(payload)
	_, err := s.Push(ctx, "dev1", "auto", []model.Change{{
		ChangeID:     "c1",
		EntityType:   model.EntityBook,
		EntityID:     "uuid-1",
		Op:           model.OpUpsert,
		BaseRevision: 0,
		Payload:      rawPayload,
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Push 内部会 RefreshLibraryVersion
	v1 := lib.v
	if v1 == "" || v1 == v0 {
		t.Fatalf("version should change after adding a book: v0=%s v1=%s", v0, v1)
	}
}

func TestGetLibraryStatus(t *testing.T) {
	mem := store.NewMemorySyncStore()
	s := NewSyncService(mem, mem, mem, mem, mem, mem)
	lib := &memLibVersion{}
	s.SetLibraryStore(lib)
	ctx := context.Background()

	// 空库
	st, err := s.GetLibraryStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.BookCount != 0 {
		t.Fatalf("empty library should have 0 books, got %d", st.BookCount)
	}

	// 加一本书
	payload, _ := json.Marshal(model.BookPayload{Name: "书A", Files: []model.BookFileMeta{{RelPath: "p.jpg", Hash: "h1"}}})
	if _, err := s.Push(ctx, "dev1", "auto", []model.Change{{
		ChangeID: "c2", EntityType: model.EntityBook, EntityID: "uuid-2",
		Op: model.OpUpsert, BaseRevision: 0, Payload: payload,
	}}); err != nil {
		t.Fatal(err)
	}
	st2, _ := s.GetLibraryStatus(ctx)
	if st2.BookCount != 1 {
		t.Fatalf("expected 1 book, got %d", st2.BookCount)
	}
	if st2.BookVersion == "" {
		t.Fatal("version should be set")
	}
}
