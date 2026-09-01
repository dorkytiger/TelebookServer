package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"TelebookServer/internal/model"
)

// EventStore 事件日志存储（同步游标的数据源）。
type EventStore interface {
	// ListAfterCursor 返回 id > cursor 且状态可下发的增量事件（升序）。
	// 返回 (events, hasMore, error)；limit 为单页条数。
	ListAfterCursor(ctx context.Context, cursor, limit int64) ([]model.SyncEvent, bool, error)

	// CountByStatus 统计各状态事件数（/sync/status 用）。
	CountByStatus(ctx context.Context) (pending, conflict, failed int64, err error)
}

// PGEventStore PostgreSQL 实现。
type PGEventStore struct {
	pool Pool
}

func NewPGEventStore(pool Pool) *PGEventStore {
	return &PGEventStore{pool: pool}
}

func (s *PGEventStore) ListAfterCursor(ctx context.Context, cursor, limit int64) ([]model.SyncEvent, bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, entity_type, entity_id, op, revision, payload, device_id, created_at
		FROM sync_events
		WHERE id > $1 AND status IN ('done', 'failed')
		ORDER BY id
		LIMIT $2`,
		cursor, limit+1, // 多取一条判断 has_more
	)
	if err != nil {
		return nil, false, fmt.Errorf("query events: %w", err)
	}
	defer rows.Close()

	events := make([]model.SyncEvent, 0, limit)
	for rows.Next() {
		var e model.SyncEvent
		if err := rows.Scan(&e.ID, &e.EntityType, &e.EntityID, &e.Op, &e.Revision, &e.Payload, &e.DeviceID, &e.CreatedAt); err != nil {
			return nil, false, err
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}

	hasMore := int64(len(events)) > limit
	if hasMore {
		events = events[:limit]
	}
	return events, hasMore, nil
}

func (s *PGEventStore) CountByStatus(ctx context.Context) (pending, conflict, failed int64, err error) {
	rows, err := s.pool.Query(ctx,
		`SELECT status, COUNT(*) FROM sync_events GROUP BY status`)
	if err != nil {
		return 0, 0, 0, err
	}
	defer rows.Close()

	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return 0, 0, 0, err
		}
		switch status {
		case "pending":
			pending = count
		case "conflict":
			conflict = count
		case "failed":
			failed = count
		}
	}
	return pending, conflict, failed, rows.Err()
}

// CursorStore 设备同步游标。
type CursorStore interface {
	UpdateCursor(ctx context.Context, deviceID string, lastEventID int64) error
	GetCursor(ctx context.Context, deviceID string) (int64, error)
}

// PGCursorStore PostgreSQL 实现。
type PGCursorStore struct {
	pool Pool
}

func NewPGCursorStore(pool Pool) *PGCursorStore {
	return &PGCursorStore{pool: pool}
}

func (s *PGCursorStore) UpdateCursor(ctx context.Context, deviceID string, lastEventID int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sync_cursors (device_id, last_event_id, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (device_id) DO UPDATE SET last_event_id = $2, updated_at = now()`,
		deviceID, lastEventID,
	)
	return err
}

func (s *PGCursorStore) GetCursor(ctx context.Context, deviceID string) (int64, error) {
	var cursor int64
	err := s.pool.QueryRow(ctx,
		`SELECT last_event_id FROM sync_cursors WHERE device_id = $1`, deviceID,
	).Scan(&cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return cursor, err
}
