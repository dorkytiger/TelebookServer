package store

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"TelebookServer/internal/model"
)

// memoryEntity 内存中的实体状态。
type memoryEntity struct {
	revision int64
	deleted  bool
	payload  json.RawMessage
}

// memoryBook 内存中的书籍状态（current_book 的镜像）。
type memoryBook struct {
	revision int64
	deleted  bool
	payload  json.RawMessage
}

// memoryEvent 内存中的事件（含幂等键 + 状态）。
type memoryEvent struct {
	changeID string
	status   string // done / conflict / failed
	model.SyncEvent
}

// memoryConflict 内存中的冲突记录。
type memoryConflict struct {
	model.Conflict
	baseRevision int64
}

// MemorySyncStore 内存版同步存储：同时实现 EntityStore / EventStore / CursorStore / ConflictStore。
//
// 与 PG 实现共享同一套业务规则（乐观锁/幂等/墓碑/冲突），
// 供单测与本地无数据库调试使用。
type MemorySyncStore struct {
	mu             sync.Mutex
	entities       map[string]*memoryEntity
	books          map[string]*memoryBook
	history        []model.BookHistory
	nextHistoryID  int64
	events         []memoryEvent
	changeIDs      map[string]int64 // change_id → event_id
	nextEventID    int64
	cursors        map[string]int64 // device_id → last_event_id
	conflicts      []memoryConflict
	nextConflictID int64
}

func NewMemorySyncStore() *MemorySyncStore {
	return &MemorySyncStore{
		entities:  map[string]*memoryEntity{},
		books:     map[string]*memoryBook{},
		history:   []model.BookHistory{},
		events:    []memoryEvent{},
		changeIDs: map[string]int64{},
		cursors:   map[string]int64{},
		conflicts: []memoryConflict{},
	}
}

var _ EntityStore = (*MemorySyncStore)(nil)
var _ BookStore = (*MemorySyncStore)(nil)
var _ HistoryStore = (*MemorySyncStore)(nil)
var _ EventStore = (*MemorySyncStore)(nil)
var _ CursorStore = (*MemorySyncStore)(nil)
var _ ConflictStore = (*MemorySyncStore)(nil)

// ApplyChange 与 PG 版一致的规则：幂等 → 锁读 → 乐观锁校验 → 更新 → 写事件。
func (s *MemorySyncStore) ApplyChange(_ context.Context, c *model.Change, deviceID string) (*ChangeOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. 幂等：change_id 已存在 → 重放
	if _, ok := s.changeIDs[c.ChangeID]; ok {
		return &ChangeOutcome{Accepted: true, Reason: model.ReasonDuplicate}, nil
	}

	key := c.EntityType + "|" + c.EntityID
	ent, ok := s.entities[key]

	switch {
	case !ok && c.Op == model.OpDelete:
		// 删除不存在的实体：无操作
		return &ChangeOutcome{Accepted: true}, nil

	case !ok && c.BaseRevision != 0:
		// 新实体必须以 base=0 创建 → 冲突（无当前版本可锁，直接拒绝）
		return &ChangeOutcome{Accepted: false, Reason: model.ReasonConflict}, nil

	case !ok:
		// 新建：revision = 1
		s.entities[key] = &memoryEntity{revision: 1, payload: c.Payload}
		return s.appendEventLocked(c, deviceID, 1, "done"), nil

	case c.BaseRevision != ent.revision:
		// 冲突：写 conflict 事件 + conflicts 记录
		return s.appendConflictLocked(c, deviceID, ent.revision), nil

	default:
		newRev := ent.revision + 1
		if c.Op == model.OpDelete {
			ent.deleted = true
		} else {
			ent.deleted = false
			ent.payload = c.Payload
		}
		ent.revision = newRev
		return s.appendEventLocked(c, deviceID, newRev, "done"), nil
	}
}

// ForceUpsert 冲突解决时的服务器权威写入。
func (s *MemorySyncStore) ForceUpsert(_ context.Context, entityType, entityID string, payload json.RawMessage, deviceID string) (*ChangeOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := entityType + "|" + entityID
	ent, ok := s.entities[key]
	var newRev int64
	if !ok {
		s.entities[key] = &memoryEntity{revision: 1, payload: payload}
		newRev = 1
	} else {
		ent.revision++
		ent.deleted = false
		ent.payload = payload
		newRev = ent.revision
	}
	c := &model.Change{EntityType: entityType, EntityID: entityID, Op: model.OpUpsert, Payload: payload}
	return s.appendEventLocked(c, deviceID, newRev, "done"), nil
}

// ApplyBookChange 与 PG 版一致的规则：幂等 → 乐观锁 → 更新 current_book + 归档推断 + 事件。
func (s *MemorySyncStore) ApplyBookChange(_ context.Context, c *model.Change, source, deviceID string) (*ChangeOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 1. 幂等：change_id 已存在 → 重放
	if _, ok := s.changeIDs[c.ChangeID]; ok {
		return &ChangeOutcome{Accepted: true, Reason: model.ReasonDuplicate}, nil
	}

	book, ok := s.books[c.EntityID]

	switch {
	case !ok && c.Op == model.OpDelete:
		// 删除不存在的书：无操作
		return &ChangeOutcome{Accepted: true}, nil

	case !ok && c.BaseRevision != 0:
		// 新书必须以 base=0 创建 → 冲突
		return &ChangeOutcome{Accepted: false, Reason: model.ReasonConflict}, nil

	case !ok:
		// 新建：revision = 1
		s.books[c.EntityID] = &memoryBook{revision: 1, payload: c.Payload}
		return s.appendEventLocked(c, deviceID, 1, "done"), nil

	case c.BaseRevision != book.revision:
		// 冲突
		return s.appendBookConflictLocked(c, deviceID, book.revision), nil

	default:
		newRev := book.revision + 1
		if c.Op == model.OpDelete {
			book.deleted = true
		} else {
			book.deleted = false
			book.payload = c.Payload
		}
		book.revision = newRev
		return s.appendEventLocked(c, deviceID, newRev, "done"), nil
	}
}

// ForceUpsertBook 冲突解决时的服务器权威写入（内存版）。
func (s *MemorySyncStore) ForceUpsertBook(_ context.Context, entityID string, payload json.RawMessage, deviceID string) (*ChangeOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	book, ok := s.books[entityID]
	var newRev int64
	if !ok {
		s.books[entityID] = &memoryBook{revision: 1, payload: payload}
		newRev = 1
	} else {
		book.revision++
		book.deleted = false
		book.payload = payload
		newRev = book.revision
	}
	c := &model.Change{ChangeID: "force-" + entityID, EntityType: model.EntityBook, EntityID: entityID, Op: model.OpUpsert, Payload: payload}
	return s.appendEventLocked(c, deviceID, newRev, "done"), nil
}

// RestoreLibrary 整库恢复（内存版）：快照书 upsert、缺失书墓碑 + 事件。
func (s *MemorySyncStore) RestoreLibrary(_ context.Context, snapshot json.RawMessage, deviceID string) (*model.BookRestoreResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var items []model.BookSnapshotItem
	if err := json.Unmarshal(snapshot, &items); err != nil {
		return nil, err
	}

	current := map[string]bool{}
	for uuid := range s.books {
		if !s.books[uuid].deleted {
			current[uuid] = true
		}
	}

	var maxRev int64
	for _, item := range items {
		payload := snapshotItemPayload(item)
		var newRev int64
		if book, ok := s.books[item.UUID]; ok {
			book.revision++
			book.deleted = false
			book.payload = payload
			newRev = book.revision
		} else {
			s.books[item.UUID] = &memoryBook{revision: 1, payload: payload}
			newRev = 1
		}
		if newRev > maxRev {
			maxRev = newRev
		}
		c := &model.Change{ChangeID: "restore-" + item.UUID, EntityType: model.EntityBook, EntityID: item.UUID, Op: model.OpUpsert, Payload: payload}
		s.appendEventLocked(c, deviceID, newRev, "done")
		delete(current, item.UUID)
	}
	for uuid := range current {
		book := s.books[uuid]
		book.revision++
		book.deleted = true
		c := &model.Change{ChangeID: "restore-del-" + uuid, EntityType: model.EntityBook, EntityID: uuid, Op: model.OpDelete, Payload: nil}
		s.appendEventLocked(c, deviceID, book.revision, "done")
		if book.revision > maxRev {
			maxRev = book.revision
		}
	}
	return &model.BookRestoreResult{Restored: len(items), Revision: maxRev}, nil
}

// SnapshotLibrary 构建当前整库快照（内存版）。
func (s *MemorySyncStore) SnapshotLibrary(_ context.Context) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	items := []model.BookSnapshotItem{}
	for uuid, book := range s.books {
		if book.deleted {
			continue
		}
		var coverHash string
		var files json.RawMessage = json.RawMessage("[]")
		if len(book.payload) > 0 {
			var p model.BookPayload
			_ = json.Unmarshal(book.payload, &p)
			coverHash = p.CoverHash
			if b, err := json.Marshal(p.Files); err == nil {
				files = b
			}
		}
		var p model.BookPayload
		if len(book.payload) > 0 {
			_ = json.Unmarshal(book.payload, &p)
		}
		items = append(items, model.BookSnapshotItem{
			UUID:        uuid,
			Name:        p.Name,
			CurrentPage: p.CurrentPage,
			CoverHash:   coverHash,
			Files:       files,
		})
	}
	return json.Marshal(items)
}

// InsertSnapshot 存一条整库快照记录（客户端驱动）。
func (s *MemorySyncStore) InsertSnapshot(_ context.Context, opType, tag string, snapshot json.RawMessage) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextHistoryID++
	s.history = append(s.history, model.BookHistory{
		ID:        s.nextHistoryID,
		OpType:    opType,
		Tag:       tag,
		Payload:   snapshot,
		CreatedAt: time.Now().UTC(),
	})
	return s.nextHistoryID, nil
}

// appendBookConflictLocked 写书冲突事件 + 冲突记录（调用方持锁）。
func (s *MemorySyncStore) appendBookConflictLocked(c *model.Change, deviceID string, currentRev int64) *ChangeOutcome {
	eventOutcome := s.appendEventLocked(c, deviceID, currentRev, "conflict")

	s.nextConflictID++
	conflictID := s.nextConflictID
	serverPayload := s.books[c.EntityID].payload
	s.conflicts = append(s.conflicts, memoryConflict{
		Conflict: model.Conflict{
			ID:             conflictID,
			EntityType:     c.EntityType,
			EntityID:       c.EntityID,
			LocalPayload:   c.Payload,
			ServerPayload:  serverPayload,
			ServerRevision: currentRev,
			Status:         model.ConflictOpen,
			CreatedAt:      time.Now().UTC(),
		},
		baseRevision: c.BaseRevision,
	})
	return &ChangeOutcome{
		Accepted:   false,
		Reason:     model.ReasonConflict,
		EventID:    eventOutcome.EventID,
		ConflictID: conflictID,
	}
}

// ListHistory 列出归档（bookID 为空则全部），按 id 倒序。
func (s *MemorySyncStore) ListHistory(_ context.Context, limit int) ([]model.BookHistory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if limit <= 0 {
		limit = 500
	}
	out := make([]model.BookHistory, 0, len(s.history))
	for i := len(s.history) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, s.history[i])
	}
	return out, nil
}

// GetHistory 读取单条归档。
func (s *MemorySyncStore) GetHistory(_ context.Context, id int64) (*model.BookHistory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.history {
		if s.history[i].ID == id {
			h := s.history[i]
			return &h, nil
		}
	}
	return nil, ErrHistoryNotFound
}

// appendEventLocked 写入 done 事件并返回结果（调用方持锁）。
func (s *MemorySyncStore) appendEventLocked(c *model.Change, deviceID string, revision int64, status string) *ChangeOutcome {
	s.nextEventID++
	id := s.nextEventID
	s.changeIDs[c.ChangeID] = id
	s.events = append(s.events, memoryEvent{
		changeID: c.ChangeID,
		status:   status,
		SyncEvent: model.SyncEvent{
			ID:         id,
			EntityType: c.EntityType,
			EntityID:   c.EntityID,
			Op:         c.Op,
			Payload:    c.Payload,
			DeviceID:   deviceID,
			CreatedAt:  time.Now().UTC(),
		},
	})
	return &ChangeOutcome{Accepted: true, Revision: revision, EventID: id}
}

// appendConflictLocked 写 conflict 事件 + 冲突记录（调用方持锁）。
func (s *MemorySyncStore) appendConflictLocked(c *model.Change, deviceID string, currentRev int64) *ChangeOutcome {
	eventOutcome := s.appendEventLocked(c, deviceID, currentRev, "conflict")

	s.nextConflictID++
	conflictID := s.nextConflictID
	s.conflicts = append(s.conflicts, memoryConflict{
		Conflict: model.Conflict{
			ID:             conflictID,
			EntityType:     c.EntityType,
			EntityID:       c.EntityID,
			LocalPayload:   c.Payload,
			ServerPayload:  s.entities[c.EntityType+"|"+c.EntityID].payload,
			ServerRevision: currentRev,
			Status:         model.ConflictOpen,
			CreatedAt:      time.Now().UTC(),
		},
		baseRevision: c.BaseRevision,
	})
	return &ChangeOutcome{
		Accepted:   false,
		Reason:     model.ReasonConflict,
		EventID:    eventOutcome.EventID,
		ConflictID: conflictID,
	}
}

// ListAfterCursor 返回 id > cursor 的事件（升序）。
func (s *MemorySyncStore) ListAfterCursor(_ context.Context, cursor, limit int64) ([]model.SyncEvent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]model.SyncEvent, 0, limit)
	for _, e := range s.events {
		if e.ID <= cursor {
			continue
		}
		if e.status != "done" && e.status != "failed" {
			continue // 冲突事件只进冲突列表，不下发
		}
		if int64(len(result)) >= limit {
			return result, true, nil
		}
		result = append(result, e.SyncEvent)
	}
	return result, false, nil
}

// CountByStatus 事件状态统计（内存实现事件均为 done）。
func (s *MemorySyncStore) CountByStatus(_ context.Context) (pending, conflict, failed int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return 0, 0, 0, nil
}

// UpdateCursor 记录设备游标。
func (s *MemorySyncStore) UpdateCursor(_ context.Context, deviceID string, lastEventID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[deviceID] = lastEventID
	return nil
}

// GetCursor 读取设备游标。
func (s *MemorySyncStore) GetCursor(_ context.Context, deviceID string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[deviceID], nil
}

// ListOpen 返回所有未解决冲突。
func (s *MemorySyncStore) ListOpen(_ context.Context) ([]model.Conflict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]model.Conflict, 0, len(s.conflicts))
	for _, c := range s.conflicts {
		if c.Status == model.ConflictOpen {
			out = append(out, c.Conflict)
		}
	}
	return out, nil
}

// Get 读取单条冲突。
func (s *MemorySyncStore) Get(_ context.Context, id int64) (*model.Conflict, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.conflicts {
		if s.conflicts[i].ID == id {
			c := s.conflicts[i].Conflict
			return &c, nil
		}
	}
	return nil, ErrConflictNotFound
}

// MarkResolved 标记冲突已解决。
func (s *MemorySyncStore) MarkResolved(_ context.Context, id int64, deviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.conflicts {
		if s.conflicts[i].ID == id {
			if s.conflicts[i].Status != model.ConflictOpen {
				return ErrConflictResolved
			}
			s.conflicts[i].Status = model.ConflictResolved
			return nil
		}
	}
	return ErrConflictNotFound
}
