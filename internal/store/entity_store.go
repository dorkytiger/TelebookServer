package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"TelebookServer/internal/model"
)

// ChangeOutcome 一次实体变更的判定结果。
type ChangeOutcome struct {
	Accepted   bool
	Revision   int64
	EventID    int64
	Reason     string // "" | model.ReasonDuplicate | model.ReasonConflict
	ConflictID int64  // 冲突发生时对应的 conflicts.id
}

// EntityStore 实体状态存储。
// ApplyChange 必须原子完成"校验乐观锁 + 更新实体 + 写事件"，
// 保证并发下 revision 单调且事件不丢失。
type EntityStore interface {
	ApplyChange(ctx context.Context, c *model.Change, deviceID string) (*ChangeOutcome, error)
	// ForceUpsert 服务器权威写入（冲突解决用）：无视乐观锁，revision+1 并写 done 事件。
	ForceUpsert(ctx context.Context, entityType, entityID string, payload json.RawMessage, deviceID string) (*ChangeOutcome, error)
}

// PGEntityStore PostgreSQL 实现（单事务）。
type PGEntityStore struct {
	pool Pool
}

func NewPGEntityStore(pool Pool) *PGEntityStore {
	return &PGEntityStore{pool: pool}
}

func (s *PGEntityStore) ApplyChange(ctx context.Context, c *model.Change, deviceID string) (*ChangeOutcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // commit 成功后再 Rollback 是无害 no-op

	// 1. 幂等：change_id 已存在 → 重放，直接接受且不重复写事件
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM sync_events WHERE change_id = $1)`, c.ChangeID,
	).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return &ChangeOutcome{Accepted: true, Reason: model.ReasonDuplicate}, nil
	}

	// 2. 锁定实体（FOR UPDATE），读取当前版本
	var currentRev int64
	var deleted bool
	err = tx.QueryRow(ctx,
		`SELECT revision, deleted FROM entities WHERE entity_type = $1 AND entity_id = $2 FOR UPDATE`,
		c.EntityType, c.EntityID,
	).Scan(&currentRev, &deleted)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 实体不存在
		if c.Op == model.OpDelete {
			// 删除不存在的实体：无操作，视为已接受（幂等）
			return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true})
		}
		if c.BaseRevision != 0 {
			// 新实体必须以 base=0 创建，否则视为冲突
			return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: false, Reason: model.ReasonConflict})
		}
		// 新建：revision = 1
		if _, err := tx.Exec(ctx,
			`INSERT INTO entities (entity_type, entity_id, revision, deleted, payload)
			 VALUES ($1, $2, 1, false, $3)`,
			c.EntityType, c.EntityID, normalizePayload(c.Payload),
		); err != nil {
			return nil, err
		}
		eventID, err := insertEvent(ctx, tx, c.ChangeID, c.EntityType, c.EntityID, 1, deviceID, c.Op, c.Payload, "done")
		if err != nil {
			return nil, err
		}
		return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true, Revision: 1, EventID: eventID})

	case err != nil:
		return nil, err

	default:
		// 实体存在：乐观锁校验
		if c.BaseRevision != currentRev {
			// 冲突：写 conflict 事件 + conflicts 记录
			conflictID, err := s.insertConflict(ctx, tx, c, deviceID, currentRev)
			if err != nil {
				return nil, err
			}
			return commitOutcome(ctx, tx, &ChangeOutcome{
				Accepted: false, Reason: model.ReasonConflict, ConflictID: conflictID,
			})
		}

		newRev := currentRev + 1
		if c.Op == model.OpDelete {
			// 墓碑：标记删除，保留 payload 快照便于追溯
			if _, err := tx.Exec(ctx,
				`UPDATE entities SET revision = $1, deleted = true, updated_at = now() WHERE entity_type = $2 AND entity_id = $3`,
				newRev, c.EntityType, c.EntityID,
			); err != nil {
				return nil, err
			}
		} else {
			if _, err := tx.Exec(ctx,
				`UPDATE entities SET revision = $1, deleted = false, payload = $2, updated_at = now() WHERE entity_type = $3 AND entity_id = $4`,
				newRev, normalizePayload(c.Payload), c.EntityType, c.EntityID,
			); err != nil {
				return nil, err
			}
		}

		eventID, err := insertEvent(ctx, tx, c.ChangeID, c.EntityType, c.EntityID, newRev, deviceID, c.Op, c.Payload, "done")
		if err != nil {
			return nil, err
		}
		return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true, Revision: newRev, EventID: eventID})
	}
}

// ForceUpsert 冲突解决时的服务器权威写入。
func (s *PGEntityStore) ForceUpsert(ctx context.Context, entityType, entityID string, payload json.RawMessage, deviceID string) (*ChangeOutcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var newRev int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO entities (entity_type, entity_id, revision, deleted, payload)
		VALUES ($1, $2, 1, false, $3)
		ON CONFLICT (entity_type, entity_id)
		DO UPDATE SET revision = entities.revision + 1, deleted = false,
		              payload = EXCLUDED.payload, updated_at = now()
		RETURNING revision`,
		entityType, entityID, normalizePayload(payload),
	).Scan(&newRev); err != nil {
		return nil, err
	}

	eventID, err := insertEvent(ctx, tx, serverChangeID(), entityType, entityID, newRev, deviceID, model.OpUpsert, payload, "done")
	if err != nil {
		return nil, err
	}
	return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true, Revision: newRev, EventID: eventID})
}

// insertConflict 在事务内写冲突事件 + conflicts 记录，返回 conflicts.id。
func (s *PGEntityStore) insertConflict(ctx context.Context, tx pgx.Tx, c *model.Change, deviceID string, currentRev int64) (int64, error) {
	var eventID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO sync_events (change_id, entity_type, entity_id, revision, device_id, op, payload, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'conflict')
		RETURNING id`,
		c.ChangeID, c.EntityType, c.EntityID, currentRev, deviceID, c.Op, c.Payload,
	).Scan(&eventID); err != nil {
		return 0, err
	}

	var conflictID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO conflicts (event_id, entity_type, entity_id, base_revision, current_revision)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		eventID, c.EntityType, c.EntityID, c.BaseRevision, currentRev,
	).Scan(&conflictID); err != nil {
		return 0, err
	}
	return conflictID, nil
}

// commitOutcome 提交事务并返回结果。
func commitOutcome(ctx context.Context, tx pgx.Tx, o *ChangeOutcome) (*ChangeOutcome, error) {
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return o, nil
}

// insertEvent 事务内插入事件，返回事件 ID。
func insertEvent(ctx context.Context, tx pgx.Tx, changeID, entityType, entityID string, revision int64, deviceID, op string, payload json.RawMessage, status string) (int64, error) {
	var eventID int64
	err := tx.QueryRow(ctx, `
		INSERT INTO sync_events (change_id, entity_type, entity_id, revision, device_id, op, payload, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		changeID, entityType, entityID, revision, deviceID, op, payload, status,
	).Scan(&eventID)
	return eventID, err
}

// serverChangeID 服务器内部事件（冲突解决等）生成幂等键。
func serverChangeID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("server-%d", time.Now().UnixNano())
	}
	return "server-" + hex.EncodeToString(b)
}

// normalizePayload 空 payload 统一为 NULL。
func normalizePayload(p json.RawMessage) any {
	if len(p) == 0 || string(p) == "null" {
		return nil
	}
	return p
}
