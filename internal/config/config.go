package config

import (
	"errors"
	"os"
	"time"

	"github.com/joho/godotenv"
)

// Config 服务配置，全部来自环境变量（开发时可放 .env 文件）。
type Config struct {
	HTTPAddr    string        // HTTP 监听地址，默认 :8080
	DatabaseURL string        // PostgreSQL 连接串
	SyncSecret  string        // 连接密钥（客户端注册设备用）
	JWTSecret   string        // JWT 签名密钥
	JWTTTL      time.Duration // access token 有效期（短期，建议 2h）
	RefreshTTL  time.Duration // refresh token 有效期，默认 30 天
	RedisAddr   string        // Redis 地址（refresh token 存储；空则用内存实现）
	Version     string        // 服务版本（/ping 返回）

	// MinIO（M4 文件同步；未配置时文件接口返回 503）
	MinIOEndpoint       string
	MinIOAccessKey      string
	MinIOSecretKey      string
	MinIOBucket         string
	MinIOUseSSL         bool
	MinIOPublicPort     string // MinIO 对外端口（host 从请求自动推断）；默认 9000
	MinIOPublicEndpoint string // 完整公网地址 host:port，显式覆盖自动推断（可选）
}

func Load() (*Config, error) {
	// 开发环境：存在 .env 时自动加载（生产建议直接用环境变量）
	_ = godotenv.Load()

	cfg := &Config{
		HTTPAddr:    getenv("HTTP_ADDR", ":8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		SyncSecret:  os.Getenv("SYNC_SECRET"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
		JWTTTL:      30 * 24 * time.Hour,
		RefreshTTL:  30 * 24 * time.Hour,
		Version:     "0.1.0",

		RedisAddr: os.Getenv("REDIS_ADDR"),

		MinIOEndpoint:  os.Getenv("MINIO_ENDPOINT"),
		MinIOAccessKey: os.Getenv("MINIO_ACCESS_KEY"),
		MinIOSecretKey: os.Getenv("MINIO_SECRET_KEY"),
		MinIOBucket:    getenv("MINIO_BUCKET", "telebook"),
		MinIOUseSSL:    os.Getenv("MINIO_USE_SSL") == "true",
		// host 自动从请求推断（见 file_handlers），只需配对外端口
		MinIOPublicPort:     getenv("MINIO_PUBLIC_PORT", "9000"),
		MinIOPublicEndpoint: os.Getenv("MINIO_PUBLIC_ENDPOINT"),
	}

	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL is required")
	}
	if cfg.SyncSecret == "" {
		return nil, errors.New("SYNC_SECRET is required")
	}
	if cfg.JWTSecret == "" {
		return nil, errors.New("JWT_SECRET is required")
	}
	if v := os.Getenv("JWT_REFRESH_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.RefreshTTL = d
		}
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
