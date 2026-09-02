package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool 数据访问的最小接口（*pgxpool.Pool 满足），便于测试替换。
type Pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Open 建立 PostgreSQL 连接池。
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// schema 建表语句（初始化项目，直接幂等建表，不做版本化迁移）。
// 全部使用 IF NOT EXISTS，重复启动安全。
var schema = []string{
	// 设备（单用户，认证粒度为设备）
	`CREATE TABLE IF NOT EXISTS devices (
		id           TEXT PRIMARY KEY,
		name         TEXT NOT NULL,
		platform     TEXT NOT NULL,
		key_hash     TEXT NOT NULL,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// 书籍当前态（current_book 是同步的当前视图）
	`CREATE TABLE IF NOT EXISTS current_book (
		entity_id    TEXT PRIMARY KEY,
		name         TEXT NOT NULL,
		current_page INTEGER NOT NULL DEFAULT 0,
		cover_hash   TEXT,
		files        JSONB NOT NULL DEFAULT '[]'::jsonb,
		revision     BIGINT NOT NULL DEFAULT 0,
		-- 进度独立 revision（§4）：只增不减；进度更新不改 revision/book_version
		progress_revision BIGINT NOT NULL DEFAULT 0,
		deleted      BOOLEAN NOT NULL DEFAULT false,
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// 通用实体当前态（非书籍实体，预留）
	`CREATE TABLE IF NOT EXISTS entities (
		entity_type TEXT NOT NULL,
		entity_id   TEXT NOT NULL,
		revision    BIGINT NOT NULL DEFAULT 0,
		deleted     BOOLEAN NOT NULL DEFAULT false,
		payload     JSONB,
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (entity_type, entity_id)
	)`,

	// 事件流（push/pull 同步的持久化日志）
	`CREATE TABLE IF NOT EXISTS sync_events (
		id          BIGSERIAL PRIMARY KEY,
		change_id   TEXT NOT NULL,
		entity_type TEXT NOT NULL,
		entity_id   TEXT NOT NULL,
		revision    BIGINT NOT NULL,
		device_id   TEXT NOT NULL,
		op          TEXT NOT NULL,
		payload     JSONB,
		status      TEXT NOT NULL DEFAULT 'done',
		created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
	`CREATE INDEX IF NOT EXISTS idx_sync_events_entity ON sync_events (entity_type, entity_id)`,
	`CREATE INDEX IF NOT EXISTS idx_sync_events_status_id ON sync_events (status, id)`,

	// 每设备的游标（增量拉取位置）
	`CREATE TABLE IF NOT EXISTS sync_cursors (
		device_id     TEXT PRIMARY KEY,
		last_event_id BIGINT NOT NULL DEFAULT 0,
		updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// 冲突（乐观锁失败时记录，供客户端选择解决策略）
	`CREATE TABLE IF NOT EXISTS conflicts (
		id               BIGSERIAL PRIMARY KEY,
		event_id         BIGINT NOT NULL REFERENCES sync_events(id),
		entity_type      TEXT NOT NULL,
		entity_id        TEXT NOT NULL,
		base_revision    BIGINT NOT NULL,
		current_revision BIGINT NOT NULL,
		status           TEXT NOT NULL DEFAULT 'open',
		resolved_by      TEXT,
		resolved_at      TIMESTAMPTZ,
		created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// 整库快照历史（归档：操作后的整库状态）
	`CREATE TABLE IF NOT EXISTS book_history (
		id         BIGSERIAL PRIMARY KEY,
		op_type    TEXT NOT NULL,
		tag        TEXT NOT NULL DEFAULT '',
		payload    JSONB NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// 库级元数据（单行）：整库版本 hash（§4，不含进度）。
	// 每次 current_book 变化时重算，供并发检测 / 初始化同步版本匹配。
	`CREATE TABLE IF NOT EXISTS library_meta (
		id           INTEGER PRIMARY KEY CHECK (id = 1),
		book_version TEXT NOT NULL DEFAULT '',
		updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// 书籍上传任务（§2.1.3 上传状态机；uuid 由服务器分配，§6）。
	`CREATE TABLE IF NOT EXISTS book_upload (
		uuid         TEXT PRIMARY KEY,
		name         TEXT NOT NULL,
		status       TEXT NOT NULL DEFAULT 'uploading', -- uploading / done / failed
		total_files  INTEGER NOT NULL DEFAULT 0,
		done_files   INTEGER NOT NULL DEFAULT 0,
		data_version TEXT NOT NULL DEFAULT '',
		device_id    TEXT NOT NULL DEFAULT '',
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,

	// 书籍上传任务的单文件进度（断点续传）。
	`CREATE TABLE IF NOT EXISTS book_upload_file (
		upload_uuid TEXT NOT NULL,
		rel_path    TEXT NOT NULL,
		hash        TEXT NOT NULL,
		size        BIGINT NOT NULL DEFAULT 0,
		status      TEXT NOT NULL DEFAULT 'pending', -- pending / done
		PRIMARY KEY (upload_uuid, rel_path)
	)`,

	// 文件元数据（SHA-256 内容寻址，去重）
	`CREATE TABLE IF NOT EXISTS file_meta (
		hash         TEXT PRIMARY KEY,
		size         BIGINT NOT NULL DEFAULT 0,
		mime_type    TEXT NOT NULL DEFAULT '',
		created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
		last_used_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`,
}

// EnsureSchema 启动时幂等建表（表不存在才创建，重复执行安全）。
func EnsureSchema(ctx context.Context, pool Pool) error {
	for _, stmt := range schema {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ensure schema: %w", err)
		}
	}
	return nil
}

// MigrateUp 兼容旧调用（客户端代码/文档引用）：改为幂等建表。
func MigrateUp(databaseURL string) error {
	ctx := context.Background()
	pool, err := Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return EnsureSchema(ctx, pool)
}
