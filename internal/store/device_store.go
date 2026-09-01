package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"TelebookServer/internal/model"
)

// DeviceStore 设备存储接口（便于测试时替换为内存实现）。
type DeviceStore interface {
	CreateDevice(ctx context.Context, d *model.Device) error
	GetDeviceByID(ctx context.Context, id string) (*model.Device, error)
	TouchDevice(ctx context.Context, id string) error
}

// ErrDeviceNotFound 设备不存在。
var ErrDeviceNotFound = errors.New("device not found")

// PGDeviceStore PostgreSQL 实现。
type PGDeviceStore struct {
	pool *pgxpool.Pool
}

func NewPGDeviceStore(pool *pgxpool.Pool) *PGDeviceStore {
	return &PGDeviceStore{pool: pool}
}

func (s *PGDeviceStore) CreateDevice(ctx context.Context, d *model.Device) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO devices (id, name, platform, key_hash) VALUES ($1, $2, $3, $4)`,
		d.ID, d.Name, d.Platform, d.KeyHash,
	)
	return err
}

func (s *PGDeviceStore) GetDeviceByID(ctx context.Context, id string) (*model.Device, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, name, platform, key_hash, created_at, last_seen_at FROM devices WHERE id = $1`,
		id,
	)
	var d model.Device
	if err := row.Scan(&d.ID, &d.Name, &d.Platform, &d.KeyHash, &d.CreatedAt, &d.LastSeenAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDeviceNotFound
		}
		return nil, err
	}
	return &d, nil
}

func (s *PGDeviceStore) TouchDevice(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE devices SET last_seen_at = now() WHERE id = $1`, id)
	return err
}
