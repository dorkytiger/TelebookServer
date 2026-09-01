package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"TelebookServer/internal/model"
)

// ErrHistoryNotFound 归档记录不存在。
var ErrHistoryNotFound = errors.New("history not found")

// HistoryStore 归档（book_history，整库快照）存储。
//
// 快照由客户端在敏感操作前捕获、操作成功后同步上来；
// 恢复 = 用快照整体替换 current_book（见 BookStore.RestoreLibrary）。
type HistoryStore interface {
	// InsertSnapshot 存一条整库快照记录。
	InsertSnapshot(ctx context.Context, opType, tag string, snapshot json.RawMessage) (int64, error)
	// ListHistory 列出归档，按时间倒序。
	ListHistory(ctx context.Context, limit int) ([]model.BookHistory, error)
	// GetHistory 读取单条归档。
	GetHistory(ctx context.Context, id int64) (*model.BookHistory, error)
}

// PGHostoryStore PostgreSQL 实现。
type PGHostoryStore struct {
	pool Pool
}

func NewPGHistoryStore(pool Pool) *PGHostoryStore {
	return &PGHostoryStore{pool: pool}
}

func (s *PGHostoryStore) InsertSnapshot(ctx context.Context, opType, tag string, snapshot json.RawMessage) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO book_history (op_type, tag, payload)
		VALUES ($1, $2, $3)
		RETURNING id`,
		opType, tag, snapshot,
	).Scan(&id)
	return id, err
}

func (s *PGHostoryStore) ListHistory(ctx context.Context, limit int) ([]model.BookHistory, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, op_type, tag, payload, created_at
		FROM book_history
		ORDER BY id DESC
		LIMIT $1`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer rows.Close()

	history := []model.BookHistory{}
	for rows.Next() {
		var h model.BookHistory
		if err := rows.Scan(&h.ID, &h.OpType, &h.Tag, &h.Payload, &h.CreatedAt); err != nil {
			return nil, err
		}
		history = append(history, h)
	}
	return history, rows.Err()
}

func (s *PGHostoryStore) GetHistory(ctx context.Context, id int64) (*model.BookHistory, error) {
	var h model.BookHistory
	err := s.pool.QueryRow(ctx, `
		SELECT id, op_type, tag, payload, created_at
		FROM book_history WHERE id = $1`,
		id,
	).Scan(&h.ID, &h.OpType, &h.Tag, &h.Payload, &h.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrHistoryNotFound
	}
	return &h, err
}
