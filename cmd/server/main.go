package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"

	"TelebookServer/internal/api"
	"TelebookServer/internal/config"
	"TelebookServer/internal/service"
	"TelebookServer/internal/store"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("server exited with error: %v", err)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx := context.Background()

	// 数据库连接 + 自动迁移（M1 只建基础表）
	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := store.MigrateUp(cfg.DatabaseURL); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}

	// 依赖装配
	deviceStore := store.NewPGDeviceStore(pool)
	jwtSvc := service.NewJWTService(cfg.JWTSecret, cfg.JWTTTL)

	// refresh token 存储：配了 Redis 用 Redis，否则内存（重启失效）
	var tokenStore store.TokenStore = store.NewMemoryTokenStore()
	if cfg.RedisAddr != "" {
		redisStore, err := store.NewRedisTokenStore(cfg.RedisAddr)
		if err != nil {
			return err
		}
		tokenStore = redisStore
		log.Printf("redis: refresh token store ready (%s)", cfg.RedisAddr)
	}
	authSvc := service.NewAuthService(deviceStore, tokenStore, jwtSvc, cfg.SyncSecret, cfg.RefreshTTL)

	entityStore := store.NewPGEntityStore(pool)
	bookStore := store.NewPGBookStore(pool)
	historyStore := store.NewPGHistoryStore(pool)
	eventStore := store.NewPGEventStore(pool)
	cursorStore := store.NewPGCursorStore(pool)
	conflictStore := store.NewPGConflictStore(pool)
	syncSvc := service.NewSyncService(entityStore, bookStore, historyStore, eventStore, cursorStore, conflictStore)

	// 文件同步（MinIO 可选：未配置时文件接口返回 503）
	var fileSvc *service.FileService
	if cfg.MinIOEndpoint != "" {
		objStore, err := store.NewMinioObjectStore(
			cfg.MinIOEndpoint, cfg.MinIOAccessKey, cfg.MinIOSecretKey, cfg.MinIOBucket, cfg.MinIOUseSSL,
			cfg.MinIOPublicPort, cfg.MinIOPublicEndpoint,
		)
		if err != nil {
			return err
		}
		if err := objStore.EnsureBucket(ctx); err != nil {
			return fmt.Errorf("ensure minio bucket: %w", err)
		}
		fileSvc = service.NewFileService(objStore, store.NewPGFileStore(pool))
		log.Printf("minio: bucket %q ready", cfg.MinIOBucket)
	}

	router := api.NewRouter(&api.Dependencies{
		Config:  cfg,
		Auth:    authSvc,
		JWT:     jwtSvc,
		Devices: deviceStore,
		Sync:    syncSvc,
		Files:   fileSvc,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("TelebookServer %s listening on %s", cfg.Version, cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	// 优雅退出
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
