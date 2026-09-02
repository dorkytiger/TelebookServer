package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"TelebookServer/internal/model"
)

// BookStore 书籍同步存储：current_book（当前态）。
//
// 历史归档（book_history）由客户端驱动（操作前捕获整库快照），
// 服务器不再在 push 时自动归档；恢复 = 整库替换 + 事件广播。
type BookStore interface {
	ApplyBookChange(ctx context.Context, c *model.Change, source, deviceID string) (*ChangeOutcome, error)
	// ForceUpsertBook 服务器权威写入（冲突解决用）：无视乐观锁，revision+1 并写 done 事件。
	ForceUpsertBook(ctx context.Context, entityID string, payload json.RawMessage, deviceID string) (*ChangeOutcome, error)
	// RestoreLibrary 整库恢复：用快照整体替换 current_book（新增/更新/墓碑 + 事件），
	// 让所有设备 pull 后回到快照状态。
	RestoreLibrary(ctx context.Context, snapshot json.RawMessage, deviceID string) (*model.BookRestoreResult, error)
	// SnapshotLibrary 构建当前整库快照（deleted=false 的书籍数组，恢复后记 restore 用）。
	SnapshotLibrary(ctx context.Context) (json.RawMessage, error)
}

// PGBookStore PostgreSQL 实现（单事务）。
type PGBookStore struct {
	pool Pool
}

func NewPGBookStore(pool Pool) *PGBookStore {
	return &PGBookStore{pool: pool}
}

// ApplyBookChange 单事务：幂等去重 → FOR UPDATE 锁书 → 乐观锁 → 落库 + 事件。
// 历史归档由客户端驱动（操作前捕获整库快照），这里不再自动归档。
func (s *PGBookStore) ApplyBookChange(ctx context.Context, c *model.Change, source, deviceID string) (*ChangeOutcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// 1. 幂等：change_id 已存在 → 重放，直接接受
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM sync_events WHERE change_id = $1)`, c.ChangeID,
	).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true, Reason: model.ReasonDuplicate})
	}

	// 2. 锁定书籍（FOR UPDATE），读取当前版本与旧快照（重建为客户端 payload 同构 JSON）
	var currentRev int64
	var deleted bool
	var oldPayload []byte
	err = tx.QueryRow(ctx, `
		SELECT revision, deleted,
		       jsonb_build_object('name', name, 'current_page', current_page,
		                          'cover_hash', cover_hash, 'files', files)::text
		FROM current_book WHERE entity_id = $1 FOR UPDATE`,
		c.EntityID,
	).Scan(&currentRev, &deleted, &oldPayload)

	// 进度更新（op=progress，§3/§4）：只改 current_page + progress_revision，
	// 不动 revision（不参与整库版本/乐观锁）、不写快照历史。
	// 书不存在则视为已接受（进度无需创建书）。
	if c.Op == model.OpProgress {
		if err == nil {
			p := parseBookPayload(c.Payload)
			if _, err := tx.Exec(ctx, `
				UPDATE current_book
				SET current_page = $1, progress_revision = progress_revision + 1,
				    updated_at = now()
				WHERE entity_id = $2`,
				p.CurrentPage, c.EntityID,
			); err != nil {
				return nil, err
			}
			// 写 progress 事件（其它设备 pull 到并应用进度，不参与整库版本）
			if _, err := insertEvent(ctx, tx, c.ChangeID, model.EntityBook, c.EntityID,
				currentRev, deviceID, model.OpProgress, c.Payload, "done"); err != nil {
				return nil, err
			}
		}
		return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true})
	}

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// 书不存在
		if c.Op == model.OpDelete {
			return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true})
		}
		if c.BaseRevision != 0 {
			return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: false, Reason: model.ReasonConflict})
		}
		// 新建：revision = 1
		p := parseBookPayload(c.Payload)
		if _, err := tx.Exec(ctx, `
			INSERT INTO current_book (entity_id, name, current_page, cover_hash, files, revision, deleted)
			VALUES ($1, $2, $3, $4, $5, 1, false)`,
			c.EntityID, p.Name, p.CurrentPage, nilOrString(p.CoverHash), filesJSON(p.Files),
		); err != nil {
			return nil, err
		}
		eventID, err := insertEvent(ctx, tx, c.ChangeID, model.EntityBook, c.EntityID, 1, deviceID, c.Op, c.Payload, "done")
		if err != nil {
			return nil, err
		}
		return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true, Revision: 1, EventID: eventID})

	case err != nil:
		return nil, err

	default:
		// 书存在：乐观锁校验
		if c.BaseRevision != currentRev {
			conflictID, err := insertBookConflict(ctx, tx, c, deviceID, currentRev)
			if err != nil {
				return nil, err
			}
			return commitOutcome(ctx, tx, &ChangeOutcome{
				Accepted: false, Reason: model.ReasonConflict, ConflictID: conflictID,
			})
		}

		newRev := currentRev + 1
		if c.Op == model.OpDelete {
			// 墓碑
			if _, err := tx.Exec(ctx,
				`UPDATE current_book SET revision = $1, deleted = true, updated_at = now() WHERE entity_id = $2`,
				newRev, c.EntityID,
			); err != nil {
				return nil, err
			}
		} else {
			p := parseBookPayload(c.Payload)
			if _, err := tx.Exec(ctx, `
				UPDATE current_book
				SET revision = $1, deleted = false, name = $2, current_page = $3,
				    cover_hash = $4, files = $5, updated_at = now()
				WHERE entity_id = $6`,
				newRev, p.Name, p.CurrentPage, nilOrString(p.CoverHash), filesJSON(p.Files), c.EntityID,
			); err != nil {
				return nil, err
			}
		}

		eventID, err := insertEvent(ctx, tx, c.ChangeID, model.EntityBook, c.EntityID, newRev, deviceID, c.Op, c.Payload, "done")
		if err != nil {
			return nil, err
		}
		return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true, Revision: newRev, EventID: eventID})
	}
}

// ForceUpsertBook 冲突解决时的服务器权威写入（current_book）。
func (s *PGBookStore) ForceUpsertBook(ctx context.Context, entityID string, payload json.RawMessage, deviceID string) (*ChangeOutcome, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	p := parseBookPayload(payload)
	var newRev int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO current_book (entity_id, name, current_page, cover_hash, files, revision, deleted)
		VALUES ($1, $2, $3, $4, $5, 1, false)
		ON CONFLICT (entity_id)
		DO UPDATE SET revision = current_book.revision + 1, deleted = false,
		              name = EXCLUDED.name, current_page = EXCLUDED.current_page,
		              cover_hash = EXCLUDED.cover_hash, files = EXCLUDED.files,
		              updated_at = now()
		RETURNING revision`,
		entityID, p.Name, p.CurrentPage, nilOrString(p.CoverHash), filesJSON(p.Files),
	).Scan(&newRev); err != nil {
		return nil, err
	}

	eventID, err := insertEvent(ctx, tx, serverChangeID(), model.EntityBook, entityID, newRev, deviceID, model.OpUpsert, payload, "done")
	if err != nil {
		return nil, err
	}
	return commitOutcome(ctx, tx, &ChangeOutcome{Accepted: true, Revision: newRev, EventID: eventID})
}

// RestoreLibrary 整库恢复：用快照整体替换 current_book。
//
// 覆盖语义（用户主动操作，不做乐观锁校验）：快照中的书 upsert（rev+1 / 新建 rev1），
// 当前存在但快照里没有的书墓碑（rev+1），每本书写 done 事件，所有设备 pull 后收敛。
func (s *PGBookStore) RestoreLibrary(ctx context.Context, snapshot json.RawMessage, deviceID string) (*model.BookRestoreResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var items []model.BookSnapshotItem
	if err := json.Unmarshal(snapshot, &items); err != nil {
		return nil, fmt.Errorf("parse snapshot: %w", err)
	}

	// 当前非墓碑书籍集合
	current := map[string]bool{}
	rows, err := tx.Query(ctx, `SELECT entity_id FROM current_book WHERE deleted = false`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		current[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var maxRev int64
	// 快照中的书：upsert（rev+1 / 新建 rev1）+ upsert 事件
	for _, item := range items {
		var newRev int64
		if err := tx.QueryRow(ctx, `
			INSERT INTO current_book (entity_id, name, current_page, cover_hash, files, revision, deleted)
			VALUES ($1, $2, $3, $4, $5, 1, false)
			ON CONFLICT (entity_id)
			DO UPDATE SET revision = current_book.revision + 1, deleted = false,
			              name = EXCLUDED.name, current_page = EXCLUDED.current_page,
			              cover_hash = EXCLUDED.cover_hash, files = EXCLUDED.files,
			              updated_at = now()
			RETURNING revision`,
			item.UUID, item.Name, item.CurrentPage, nilOrString(item.CoverHash), normalizePayload(item.Files),
		).Scan(&newRev); err != nil {
			return nil, err
		}
		if newRev > maxRev {
			maxRev = newRev
		}
		payload := snapshotItemPayload(item)
		if _, err := insertEvent(ctx, tx, serverChangeID(), model.EntityBook, item.UUID, newRev, deviceID, model.OpUpsert, payload, "done"); err != nil {
			return nil, err
		}
		delete(current, item.UUID)
	}

	// 快照里没有的书：墓碑 + delete 事件
	for uuid := range current {
		var newRev int64
		if err := tx.QueryRow(ctx,
			`UPDATE current_book SET revision = revision + 1, deleted = true, updated_at = now() WHERE entity_id = $1 RETURNING revision`,
			uuid,
		).Scan(&newRev); err != nil {
			return nil, err
		}
		if newRev > maxRev {
			maxRev = newRev
		}
		if _, err := insertEvent(ctx, tx, serverChangeID(), model.EntityBook, uuid, newRev, deviceID, model.OpDelete, nil, "done"); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &model.BookRestoreResult{Restored: len(items), Revision: maxRev}, nil
}

// SnapshotLibrary 构建当前整库快照（deleted=false 的书籍数组，
// 含每本书 revision 供客户端回填乐观锁基准）。
func (s *PGBookStore) SnapshotLibrary(ctx context.Context) (json.RawMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT entity_id, name, current_page, cover_hash, files, revision
		FROM current_book WHERE deleted = false ORDER BY entity_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []model.BookSnapshotItem{}
	for rows.Next() {
		var it model.BookSnapshotItem
		var cover any
		if err := rows.Scan(&it.UUID, &it.Name, &it.CurrentPage, &cover, &it.Files, &it.Revision); err != nil {
			return nil, err
		}
		// pgx 把 TEXT 列扫成 string（或 []byte），两种都兼容
		switch v := cover.(type) {
		case string:
			it.CoverHash = v
		case []byte:
			it.CoverHash = string(v)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(items)
}

// snapshotItemPayload 快照项 → 书籍 payload JSON（事件下发用）。
func snapshotItemPayload(item model.BookSnapshotItem) json.RawMessage {
	files := item.Files
	if len(files) == 0 || string(files) == "null" {
		files = json.RawMessage("[]")
	}
	b, _ := json.Marshal(map[string]any{
		"name":         item.Name,
		"current_page": item.CurrentPage,
		"cover_hash":   nilOrString(item.CoverHash),
		"files":        json.RawMessage(files),
	})
	return b
}

// parseBookPayload 解析书籍快照；失败返回零值（归档推断按无变化处理）。
func parseBookPayload(payload json.RawMessage) model.BookPayload {
	var p model.BookPayload
	if len(payload) > 0 && string(payload) != "null" {
		_ = json.Unmarshal(payload, &p)
	}
	return p
}

// filesJSON 文件清单 → JSONB 值（nil → NULL）。
func filesJSON(files []model.BookFileMeta) any {
	if files == nil {
		return nil
	}
	b, _ := json.Marshal(files)
	return b
}

// nilOrString 空串 → NULL。
func nilOrString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// insertBookConflict 事务内写冲突事件 + conflicts 记录（书籍）。
func insertBookConflict(ctx context.Context, tx pgx.Tx, c *model.Change, deviceID string, currentRev int64) (int64, error) {
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
