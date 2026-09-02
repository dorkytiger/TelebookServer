package service

import (
	"context"
	"encoding/json"
	"testing"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

func TestProgressOpDoesNotChangeRevisionOrVersion(t *testing.T) {
	mem := store.NewMemorySyncStore()
	s := NewSyncService(mem, mem, mem, mem, mem, mem)
	lib := &memLibVersion{}
	s.SetLibraryStore(lib)
	ctx := context.Background()

	// 建书
	payload, _ := json.Marshal(model.BookPayload{Name: "书A", Files: []model.BookFileMeta{{RelPath: "p.jpg", Hash: "h1"}}})
	if _, err := s.Push(ctx, "dev1", "auto", []model.Change{{
		ChangeID: "c1", EntityType: model.EntityBook, EntityID: "u1",
		Op: model.OpUpsert, BaseRevision: 0, Payload: payload,
	}}); err != nil {
		t.Fatal(err)
	}
	vBefore, _ := lib.GetBookVersion(ctx)

	// 进度更新（op=progress）
	progPayload, _ := json.Marshal(model.BookPayload{Name: "书A", CurrentPage: 5})
	if _, err := s.Push(ctx, "dev1", "auto", []model.Change{{
		ChangeID: "c2", EntityType: model.EntityBook, EntityID: "u1",
		Op: model.OpProgress, BaseRevision: 0, Payload: progPayload,
	}}); err != nil {
		t.Fatal(err)
	}

	// 整库版本不应因进度改变
	vAfter, _ := lib.GetBookVersion(ctx)
	if vBefore != vAfter {
		t.Fatalf("progress should not change library version: %s vs %s", vBefore, vAfter)
	}
}
