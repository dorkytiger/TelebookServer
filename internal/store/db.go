package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // 注册 pgx 的 database/sql 驱动（迁移用）
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

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

// MigrateUp 执行嵌入的 SQL 迁移（启动时自动，无需单独迁移步骤）。
//
// golang-migrate 的 pgx 驱动需要 *sql.DB，这里用 pgx stdlib 单独开一个
// 迁移连接，用完即关，不影响主连接池。
func MigrateUp(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open migrate connection: %w", err)
	}
	defer db.Close()

	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	driver, err := pgxmigrate.WithInstance(db, &pgxmigrate.Config{})
	if err != nil {
		return fmt.Errorf("create migrate driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("init migrate: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
