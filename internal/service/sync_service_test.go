package service

import (
	"context"
	"encoding/json"
	"testing"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

func newTestSyncService() *SyncService {
	mem := store.NewMemorySyncStore()
	return NewSyncService(mem, mem, mem, mem, mem, mem)
}

func change(id, entityType, entityID, op string, base int64, payload any) model.Change {
	var raw json.RawMessage
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	return model.Change{
		ChangeID:     id,
		EntityType:   entityType,
		EntityID:     entityID,
		Op:           op,
		BaseRevision: base,
		Payload:      raw,
	}
}

func TestPushCreateEntity(t *testing.T) {
	s := newTestSyncService()
	results, err := s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c1", "book", "book-1", model.OpUpsert, 0, map[string]string{"name": "书一"}),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !results[0].Accepted || results[0].Revision != 1 || results[0].EventID == 0 {
		t.Fatalf("bad result: %+v", results[0])
	}
}

func TestPushUpdateEntity(t *testing.T) {
	s := newTestSyncService()
	_, _ = s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c1", "book", "book-1", model.OpUpsert, 0, map[string]string{"name": "书一"}),
	})
	results, _ := s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c2", "book", "book-1", model.OpUpsert, 1, map[string]string{"name": "书一改名"}),
	})
	if !results[0].Accepted || results[0].Revision != 2 {
		t.Fatalf("bad result: %+v", results[0])
	}
}

func TestPushConflict(t *testing.T) {
	s := newTestSyncService()
	_, _ = s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c1", "book", "book-1", model.OpUpsert, 0, map[string]string{"name": "A"}),
	})
	// 另一台设备基于旧版本 base=0 提交 → 冲突
	results, _ := s.Push(context.Background(), "dev-b", "auto", []model.Change{
		change("c2", "book", "book-1", model.OpUpsert, 0, map[string]string{"name": "B"}),
	})
	if results[0].Accepted || results[0].Reason != model.ReasonConflict {
		t.Fatalf("expected conflict, got %+v", results[0])
	}
}

func TestPushIdempotentReplay(t *testing.T) {
	s := newTestSyncService()
	changes := []model.Change{
		change("c1", "book", "book-1", model.OpUpsert, 0, map[string]string{"name": "书"}),
	}
	if _, err := s.Push(context.Background(), "dev-a", "auto", changes); err != nil {
		t.Fatal(err)
	}
	// 客户端断网重试，重发同 change_id → 接受但不重复写事件
	results, err := s.Push(context.Background(), "dev-a", "auto", changes)
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].Accepted || results[0].Reason != model.ReasonDuplicate || results[0].EventID != 0 {
		t.Fatalf("expected duplicate, got %+v", results[0])
	}

	// 事件只有一条
	pull, _ := s.Pull(context.Background(), "dev-b", 0, 100)
	if len(pull.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(pull.Events))
	}
}

func TestPushDeleteTombstone(t *testing.T) {
	s := newTestSyncService()
	_, _ = s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c1", "book", "book-1", model.OpUpsert, 0, map[string]string{"name": "书"}),
	})
	results, _ := s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c2", "book", "book-1", model.OpDelete, 1, nil),
	})
	if !results[0].Accepted || results[0].Revision != 2 {
		t.Fatalf("bad delete result: %+v", results[0])
	}
	// 删除不存在的实体：接受且无事件
	results, _ = s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c3", "book", "ghost", model.OpDelete, 0, nil),
	})
	if !results[0].Accepted || results[0].EventID != 0 {
		t.Fatalf("bad noop delete result: %+v", results[0])
	}
}

func TestPushBatchPartial(t *testing.T) {
	s := newTestSyncService()
	results, err := s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c1", "book", "book-1", model.OpUpsert, 0, nil),
		change("c2", "book", "book-1", model.OpUpsert, 0, nil), // 冲突
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if !results[0].Accepted || results[1].Accepted {
		t.Fatalf("expected [accept, conflict], got %+v", results)
	}
}

func TestPullCursorAndLimit(t *testing.T) {
	s := newTestSyncService()
	_, _ = s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c1", "book", "b1", model.OpUpsert, 0, nil),
		change("c2", "book", "b2", model.OpUpsert, 0, nil),
		change("c3", "book", "b3", model.OpUpsert, 0, nil),
	})

	pull, _ := s.Pull(context.Background(), "dev-b", 0, 2)
	if len(pull.Events) != 2 || !pull.HasMore || pull.Cursor != 2 {
		t.Fatalf("bad first page: %+v", pull)
	}
	pull2, _ := s.Pull(context.Background(), "dev-b", pull.Cursor, 2)
	if len(pull2.Events) != 1 || pull2.HasMore || pull2.Cursor != 3 {
		t.Fatalf("bad second page: %+v", pull2)
	}
	// 游标已持久化
	if got, _ := s.cursors.GetCursor(context.Background(), "dev-b"); got != 3 {
		t.Fatalf("expected cursor 3, got %d", got)
	}
}

func TestPullAcrossDevices(t *testing.T) {
	s := newTestSyncService()
	// 设备 A 建书 + 改书名 + 删除另一本
	_, _ = s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c1", "book", "b1", model.OpUpsert, 0, map[string]string{"name": "书一"}),
		change("c2", "book", "b2", model.OpUpsert, 0, nil),
	})
	_, _ = s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c3", "book", "b1", model.OpUpsert, 1, map[string]string{"name": "书一改"}),
	})
	_, _ = s.Push(context.Background(), "dev-a", "auto", []model.Change{
		change("c4", "book", "b2", model.OpDelete, 1, nil),
	})

	// 设备 B 全量拉取：应有 4 条事件（含改书名与墓碑）
	pull, _ := s.Pull(context.Background(), "dev-b", 0, 100)
	if len(pull.Events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(pull.Events))
	}
	// 顺序校验：b1 创建 → b2 创建 → b1 改名 → b2 删除
	if pull.Events[0].EntityID != "b1" || pull.Events[1].EntityID != "b2" ||
		pull.Events[2].Op != model.OpUpsert || pull.Events[3].Op != model.OpDelete {
		t.Fatalf("unexpected order: %+v", pull.Events)
	}
}

// ── 历史：整库快照（客户端驱动，服务器不自动归档）──────────

func historyOf(t *testing.T, mem *store.MemorySyncStore) []model.BookHistory {
	t.Helper()
	h, err := mem.ListHistory(context.Background(), 100)
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	return h
}

// push 不再自动归档（历史由客户端在操作前捕获快照驱动）
func TestPushDoesNotAutoArchive(t *testing.T) {
	mem := store.NewMemorySyncStore()
	s := NewSyncService(mem, mem, mem, mem, mem, mem)
	_, _ = s.Push(context.Background(), "dev-a", model.SyncSourceAuto, []model.Change{
		change("c1", model.EntityBook, "book-1", model.OpUpsert, 0, map[string]any{"name": "书一"}),
	})
	_, _ = s.Push(context.Background(), "dev-a", model.SyncSourceAuto, []model.Change{
		change("c2", model.EntityBook, "book-1", model.OpUpsert, 1, map[string]any{"name": "书一改名"}),
	})
	if h := historyOf(t, mem); len(h) != 0 {
		t.Fatalf("push must not create history, got %+v", h)
	}
}

// RecordHistory：客户端把操作前快照同步为一条记录
func TestRecordHistory(t *testing.T) {
	mem := store.NewMemorySyncStore()
	s := NewSyncService(mem, mem, mem, mem, mem, mem)

	snapshot := json.RawMessage(`[{"uuid":"b1","name":"书一","current_page":0,"cover_hash":"","files":[]}]`)
	id, err := s.RecordHistory(context.Background(), model.OpImport, model.TagAuto, snapshot)
	if err != nil {
		t.Fatalf("record history: %v", err)
	}
	if id == 0 {
		t.Fatal("expected history id")
	}
	h := historyOf(t, mem)
	if len(h) != 1 || h[0].OpType != model.OpImport || h[0].Tag != model.TagAuto {
		t.Fatalf("unexpected history: %+v", h)
	}
}

// 整库恢复：快照 2 本 → push 第 3 本 → 恢复后第 3 本墓碑，快照书保留
func TestRestoreLibrary(t *testing.T) {
	mem := store.NewMemorySyncStore()
	s := NewSyncService(mem, mem, mem, mem, mem, mem)

	// 快照：2 本书
	snapshot := json.RawMessage(`[
		{"uuid":"b1","name":"书一","current_page":3,"cover_hash":"h1","files":[{"rel_path":"cover.jpg","hash":"h1","size":10}]},
		{"uuid":"b2","name":"书二","current_page":0,"cover_hash":"","files":[]}
	]`)
	historyID, err := s.RecordHistory(context.Background(), model.OpManualSync, model.TagManual, snapshot)
	if err != nil {
		t.Fatalf("record history: %v", err)
	}

	// 之后 push 了 3 本书（b1/b2/b3），其中 b3 是新的
	_, _ = s.Push(context.Background(), "dev-a", model.SyncSourceAuto, []model.Change{
		change("c1", model.EntityBook, "b1", model.OpUpsert, 0, map[string]any{"name": "书一"}),
		change("c2", model.EntityBook, "b2", model.OpUpsert, 0, map[string]any{"name": "书二"}),
		change("c3", model.EntityBook, "b3", model.OpUpsert, 0, map[string]any{"name": "书三"}),
	})

	// 恢复整库到快照
	result, err := s.RestoreBook(context.Background(), "dev-b", historyID)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Restored != 2 {
		t.Fatalf("expected 2 restored books, got %d", result.Restored)
	}

	// b3 应被墓碑，b1/b2 保留
	snap, _ := mem.SnapshotLibrary(context.Background())
	var items []model.BookSnapshotItem
	if err := json.Unmarshal(snap, &items); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	got := map[string]bool{}
	for _, it := range items {
		got[it.UUID] = true
	}
	if !got["b1"] || !got["b2"] || got["b3"] {
		t.Fatalf("restored library mismatch: %+v", got)
	}

	// 恢复后自动记一条 restore（tag=manual）历史
	h := historyOf(t, mem)
	if h[0].OpType != model.OpRestore || h[0].Tag != model.TagManual {
		t.Fatalf("expected restore(manual) history, got %+v", h[0])
	}

	// 恢复事件可 pull：设备 C 拉取到 b3 的 delete 墓碑
	pull, _ := s.Pull(context.Background(), "dev-c", 0, 100)
	hasDeleteB3 := false
	for _, e := range pull.Events {
		if e.EntityID == "b3" && e.Op == model.OpDelete {
			hasDeleteB3 = true
		}
	}
	if !hasDeleteB3 {
		t.Fatalf("expected b3 delete event after restore, got %+v", pull.Events)
	}
}

// 恢复不存在的归档 → 报错
func TestRestoreMissingHistory(t *testing.T) {
	mem := store.NewMemorySyncStore()
	s := NewSyncService(mem, mem, mem, mem, mem, mem)
	if _, err := s.RestoreBook(context.Background(), "dev-b", 99999); err == nil {
		t.Fatal("expected error for missing history")
	}
}
