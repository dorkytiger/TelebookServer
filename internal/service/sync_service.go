package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"TelebookServer/internal/model"
	"TelebookServer/internal/store"
)

// SyncService 元数据同步：push（乐观锁 + 事件写入）/ pull（游标增量）/ status / 冲突 / 归档。
type SyncService struct {
	entities   store.EntityStore
	books      store.BookStore
	history    store.HistoryStore
	events     store.EventStore
	cursors    store.CursorStore
	conflicts  store.ConflictStore
	fileVerifier FileVerifier // 整本书文件完整性校验（可选；nil 则跳过校验）
}

// FileVerifier 校验一本书的所有文件是否已在对象存储完整存在。
// 实现方应基于"实际对象大小 == 声明大小"判定（不完整/损坏 → false）。
type FileVerifier interface {
	// AllFilesComplete 返回 payload.files 是否全部完整存在。
	AllFilesComplete(ctx context.Context, files []model.BookFileMeta) (bool, error)
}

func NewSyncService(entities store.EntityStore, books store.BookStore, history store.HistoryStore, events store.EventStore, cursors store.CursorStore, conflicts store.ConflictStore) *SyncService {
	return &SyncService{
		entities:  entities,
		books:     books,
		history:   history,
		events:    events,
		cursors:   cursors,
		conflicts: conflicts,
	}
}

// SetFileVerifier 注入文件完整性校验器（main 里装配）。
func (s *SyncService) SetFileVerifier(v FileVerifier) { s.fileVerifier = v }

// ListHistory 列出归档（整库快照），按时间倒序。
func (s *SyncService) ListHistory(ctx context.Context, limit int) ([]model.BookHistory, error) {
	return s.history.ListHistory(ctx, limit)
}

// RestoreBook 整库恢复：用快照整体替换 current_book 并传播。
// 恢复成功后由服务器重建快照，记一条 restore（tag=manual）历史。
func (s *SyncService) RestoreBook(ctx context.Context, deviceID string, historyID int64) (*model.BookRestoreResult, error) {
	h, err := s.history.GetHistory(ctx, historyID)
	if err != nil {
		return nil, err
	}
	result, err := s.books.RestoreLibrary(ctx, h.Payload, deviceID)
	if err != nil {
		return nil, err
	}
	if snap, err := s.books.SnapshotLibrary(ctx); err == nil {
		_, _ = s.history.InsertSnapshot(ctx, model.OpRestore, model.TagManual, snap)
	}
	return result, nil
}

// RecordHistory 客户端驱动：操作前捕获的整库快照同步为一条历史记录。
func (s *SyncService) RecordHistory(ctx context.Context, opType, tag string, snapshot json.RawMessage) (int64, error) {
	return s.history.InsertSnapshot(ctx, opType, tag, snapshot)
}

// Push 批量提交本地变更。逐条判定，互不影响（单条失败不影响其余）。
//
// source 为同步来源（manual=手动同步会话 / auto=自动或单操作），
// 书籍变更会按 source 与内容差异推断是否写归档。
//
// 文件完整性：若注入了 FileVerifier，书事件落库前校验其全部文件已完整
// （实际大小 == 声明大小）。不完整 → 拒绝该书（Accepted=false, reason=files_incomplete），
// 驱动客户端清理垃圾文件并重传，避免把"半完成的书"写入库/事件流。
func (s *SyncService) Push(ctx context.Context, deviceID, source string, changes []model.Change) ([]model.ChangeResult, error) {
	results := make([]model.ChangeResult, 0, len(changes))
	for _, c := range changes {
		// 书事件：先校验文件完整性
		if c.EntityType == model.EntityBook && s.fileVerifier != nil && c.Op != model.OpDelete && c.Payload != nil {
			var p model.BookPayload
			if err := json.Unmarshal(c.Payload, &p); err == nil && len(p.Files) > 0 {
				ok, err := s.fileVerifier.AllFilesComplete(ctx, p.Files)
				if err != nil {
					return nil, fmt.Errorf("verify files %s/%s: %w", c.EntityType, c.EntityID, err)
				}
				if !ok {
					results = append(results, model.ChangeResult{
						EntityType: c.EntityType,
						EntityID:   c.EntityID,
						Accepted:   false,
						Reason:     model.ReasonFilesIncomplete,
					})
					continue
				}
			}
		}

		var outcome *store.ChangeOutcome
		var err error
		if c.EntityType == model.EntityBook {
			outcome, err = s.books.ApplyBookChange(ctx, &c, source, deviceID)
		} else {
			outcome, err = s.entities.ApplyChange(ctx, &c, deviceID)
		}
		if err != nil {
			return nil, fmt.Errorf("apply change %s/%s: %w", c.EntityType, c.EntityID, err)
		}
		results = append(results, model.ChangeResult{
			EntityType: c.EntityType,
			EntityID:   c.EntityID,
			Accepted:   outcome.Accepted,
			Revision:   outcome.Revision,
			EventID:    outcome.EventID,
			Reason:     outcome.Reason,
			ConflictID: outcome.ConflictID,
		})
	}
	return results, nil
}

// Pull 增量拉取：返回 cursor 之后的事件，并推进该设备的服务器端游标。
func (s *SyncService) Pull(ctx context.Context, deviceID string, cursor, limit int64) (*model.PullResult, error) {
	events, hasMore, err := s.events.ListAfterCursor(ctx, cursor, limit)
	if err != nil {
		return nil, err
	}

	last := cursor
	if len(events) > 0 {
		last = events[len(events)-1].ID
	}
	if err := s.cursors.UpdateCursor(ctx, deviceID, last); err != nil {
		return nil, fmt.Errorf("update cursor: %w", err)
	}

	return &model.PullResult{Cursor: last, HasMore: hasMore, Events: events}, nil
}

// Status 返回设备同步状态。
func (s *SyncService) Status(ctx context.Context, deviceID string) (*model.SyncStatus, error) {
	cursor, err := s.cursors.GetCursor(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	pending, _, failed, err := s.events.CountByStatus(ctx)
	if err != nil {
		return nil, err
	}
	open, err := s.conflicts.ListOpen(ctx)
	if err != nil {
		return nil, err
	}
	return &model.SyncStatus{
		Cursor:        cursor,
		PendingCount:  pending,
		ConflictCount: int64(len(open)),
		FailedCount:   failed,
	}, nil
}

// ListConflicts 返回未解决冲突列表。
func (s *SyncService) ListConflicts(ctx context.Context) ([]model.Conflict, error) {
	return s.conflicts.ListOpen(ctx)
}

var (
	// ErrConflictNotFound 冲突不存在。
	ErrConflictNotFound = store.ErrConflictNotFound
	// ErrConflictAlreadyResolved 冲突已解决。
	ErrConflictAlreadyResolved = store.ErrConflictResolved
	// ErrInvalidStrategy 无效的解决策略。
	ErrInvalidStrategy = errors.New("invalid resolve strategy")
)

// ResolveConflict 解决冲突：keep_local / keep_server / manual。
// 除 keep_server 外，胜方快照会写入实体（revision+1）并产生 done 事件，
// 让其他设备通过 pull 感知解决结果。
func (s *SyncService) ResolveConflict(ctx context.Context, deviceID string, conflictID int64, req model.ResolveRequest) error {
	conflict, err := s.conflicts.Get(ctx, conflictID)
	if err != nil {
		return err
	}
	if conflict.Status != model.ConflictOpen {
		return ErrConflictAlreadyResolved
	}

	switch req.Strategy {
	case model.StrategyKeepServer:
		// 服务器版本已是最新且已通过事件传播，无需写实体
	case model.StrategyKeepLocal:
		if len(conflict.LocalPayload) == 0 {
			return errors.New("conflict has no local payload")
		}
		if err := s.forceUpsert(ctx, conflict, conflict.LocalPayload, deviceID); err != nil {
			return err
		}
	case model.StrategyManual:
		if len(req.Payload) == 0 {
			return errors.New("payload is required for manual resolve")
		}
		if err := s.forceUpsert(ctx, conflict, req.Payload, deviceID); err != nil {
			return err
		}
	default:
		return ErrInvalidStrategy
	}

	return s.conflicts.MarkResolved(ctx, conflictID, deviceID)
}

// forceUpsert 按实体类型路由权威写入（书籍走 current_book，其余走通用 entities）。
func (s *SyncService) forceUpsert(ctx context.Context, conflict *model.Conflict, payload json.RawMessage, deviceID string) error {
	if conflict.EntityType == model.EntityBook {
		_, err := s.books.ForceUpsertBook(ctx, conflict.EntityID, payload, deviceID)
		return err
	}
	_, err := s.entities.ForceUpsert(ctx, conflict.EntityType, conflict.EntityID, payload, deviceID)
	return err
}
