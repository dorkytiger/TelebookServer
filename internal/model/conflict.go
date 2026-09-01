package model

import (
	"encoding/json"
	"time"
)

// Conflict 一条待解决的冲突（本地快照 vs 服务器快照）。
type Conflict struct {
	ID             int64           `json:"id"`
	EntityType     string          `json:"entity_type"`
	EntityID       string          `json:"entity_id"`
	LocalPayload   json.RawMessage `json:"local_payload"`  // 冲突方（失败提交）的快照
	ServerPayload  json.RawMessage `json:"server_payload"` // 服务器当前快照
	ServerRevision int64           `json:"server_revision"`
	Status         string          `json:"status"` // open | resolved
	CreatedAt      time.Time       `json:"created_at"`
}

// ResolveRequest 冲突解决请求。
type ResolveRequest struct {
	Strategy string          `json:"strategy"` // keep_local | keep_server | manual
	Payload  json.RawMessage `json:"payload"`  // manual 时必填（获胜快照）
}

// 冲突解决策略。
const (
	StrategyKeepLocal  = "keep_local"
	StrategyKeepServer = "keep_server"
	StrategyManual     = "manual"
)

// 冲突状态。
const (
	ConflictOpen     = "open"
	ConflictResolved = "resolved"
)
