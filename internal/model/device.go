package model

import "time"

// Device 已注册的设备（单用户，认证粒度为设备）。
type Device struct {
	ID         string    `json:"device_id"`
	Name       string    `json:"device_name"`
	Platform   string    `json:"platform"`
	KeyHash    string    `json:"-"` // 连接密钥哈希（bcrypt），不回传
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}
