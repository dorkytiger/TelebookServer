package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"TelebookServer/internal/model"
)

// ErrConflictNotFound 冲突记录不存在。
var ErrConflictNotFound = errors.New("conflict not found")

// ErrConflictResolved 冲突已解决。
var ErrConflictResolved = errors.New("conflict already resolved")

// ConflictStore 冲突记录存储。
type ConflictStore interface {
	// ListOpen 返回所有未解决冲突（含本地/服务器快照）。
	ListOpen(ctx context.Context) ([]model.Conflict, error)
	// Get 读取单条冲突。
	Get(ctx context.Context, id int64) (*model.Conflict, error)
	// MarkResolved 标记冲突已解决。
	MarkResolved(ctx context.Context, id int64, deviceID string) error
}

// PGConflictStore PostgreSQL 实现。
type PGConflictStore struct {
	pool Pool
}

func NewPGConflictStore(pool Pool) *PGConflictStore {
	return &PGConflictStore{pool: pool}
}

const conflictSelect = `
SELECT c.id, c.entity_type, c.entity_id,
       e.payload, COALESCE(ent.payload,
                           jsonb_build_object('name', cb.name, 'current_page', cb.current_page,
                                              'cover_hash', cb.cover_hash, 'files', cb.files)),
       c.current_revision, c.status, c.created_at
FROM conflicts c
JOIN sync_events e ON e.id = c.event_id
LEFT JOIN entities ent ON ent.entity_type = c.entity_type AND ent.entity_id = c.entity_id AND c.entity_type <> 'book'
LEFT JOIN current_book cb ON cb.entity_id = c.entity_id AND c.entity_type = 'book'`

func scanConflict(row pgx.Row) (*model.Conflict, error) {
	var c model.Conflict
	if err := row.Scan(&c.ID, &c.EntityType, &c.EntityID,
		&c.LocalPayload, &c.ServerPayload, &c.ServerRevision, &c.Status, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *PGConflictStore) ListOpen(ctx context.Context) ([]model.Conflict, error) {
	rows, err := s.pool.Query(ctx, conflictSelect+` WHERE c.status = 'open' ORDER BY c.id`)
	if err != nil {
		return nil, fmt.Errorf("query conflicts: %w", err)
	}
	defer rows.Close()

	conflicts := []model.Conflict{}
	for rows.Next() {
		c, err := scanConflict(rows)
		if err != nil {
			return nil, err
		}
		conflicts = append(conflicts, *c)
	}
	return conflicts, rows.Err()
}

func (s *PGConflictStore) Get(ctx context.Context, id int64) (*model.Conflict, error) {
	row := s.pool.QueryRow(ctx, conflictSelect+` WHERE c.id = $1`, id)
	c, err := scanConflict(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConflictNotFound
	}
	return c, err
}

func (s *PGConflictStore) MarkResolved(ctx context.Context, id int64, deviceID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE conflicts SET status = 'resolved', resolved_by = $2, resolved_at = now()
		 WHERE id = $1 AND status = 'open'`,
		id, deviceID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflictResolved
	}
	return nil
}
