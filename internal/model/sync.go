package model

import (
	"encoding/json"
	"time"
)

// Change 一次实体变更（客户端提交）。
type Change struct {
	ChangeID     string          `json:"change_id"`     // 客户端生成的 UUID（幂等键）
	EntityType   string          `json:"entity_type"`   // book / collection / progress / setting
	EntityID     string          `json:"entity_id"`     // 实体 UUID（客户端生成，跨设备稳定）
	Op           string          `json:"op"`            // upsert / delete（delete = 墓碑）
	BaseRevision int64           `json:"base_revision"` // 上次成功同步看到的版本；新实体为 0
	Payload      json.RawMessage `json:"payload"`       // 实体完整快照；delete 时可空
}

// ChangeResult 单条变更的结果。
type ChangeResult struct {
	EntityType string `json:"entity_type"`
	EntityID   string `json:"entity_id"`
	Accepted   bool   `json:"accepted"`
	Revision   int64  `json:"revision"`
	EventID    int64  `json:"event_id"`
	Reason     string `json:"reason,omitempty"` // conflict / duplicate
	ConflictID int64  `json:"conflict_id,omitempty"`
}

// SyncEvent 拉取返回的同步事件。
type SyncEvent struct {
	ID         int64           `json:"id"`
	EntityType string          `json:"entity_type"`
	EntityID   string          `json:"entity_id"`
	Op         string          `json:"op"`
	Revision   int64           `json:"revision"` // 该变更后的实体版本（客户端回写乐观锁）
	Payload    json.RawMessage `json:"payload"`
	DeviceID   string          `json:"device_id"`
	CreatedAt  time.Time       `json:"created_at"`
}

// PullResult 增量拉取结果。
type PullResult struct {
	Cursor  int64       `json:"cursor"`
	HasMore bool        `json:"has_more"`
	Events  []SyncEvent `json:"events"`
}

// SyncStatus 当前同步状态（前端列表数据源）。
type SyncStatus struct {
	Cursor        int64      `json:"cursor"`
	PendingCount  int64      `json:"pending_count"`
	ConflictCount int64      `json:"conflict_count"`
	FailedCount   int64      `json:"failed_count"`
	LastSyncedAt  *time.Time `json:"last_synced_at,omitempty"` // 尚未同步过则为空
}

// 实体操作类型。
const (
	OpUpsert = "upsert"
	OpDelete = "delete"
)

// 实体类型。
const (
	EntityBook = "book"
)

// 同步来源（push 请求级）：manual = 手动同步会话；auto = 自动/单操作。
const (
	SyncSourceManual = "manual"
	SyncSourceAuto   = "auto"
)

// 归档操作类型（book_history.op_type）。
const (
	OpImport     = "import"
	OpModify     = "modify"
	OpManualSync = "manual_sync"
	OpRestore    = "restore"
)

// 归档标签（book_history.tag）。
const (
	TagManual = "manual"
	TagAuto   = "auto"
)

// 结果原因。
const (
	ReasonConflict        = "conflict"
	ReasonDuplicate       = "duplicate"
	ReasonFilesIncomplete = "files_incomplete" // 书的文件未完整上传（服务器拒绝，客户端补传)
)

// BookFileMeta 书籍内单个文件（hash 引用，内容寻址 → 跨设备去重）。
type BookFileMeta struct {
	RelPath string `json:"rel_path"`
	Hash    string `json:"hash"`
	Size    int64  `json:"size"`
}

// BookPayload 书籍快照（服务器解析用；客户端统一 snake_case）。
type BookPayload struct {
	Name        string         `json:"name"`
	CurrentPage int            `json:"current_page"`
	CoverHash   string         `json:"cover_hash"`
	Files       []BookFileMeta `json:"files"`
}

// BookHistory 归档记录（book_history 表行）：payload = 整库快照（书籍数组）。
type BookHistory struct {
	ID        int64           `json:"id"`
	OpType    string          `json:"op_type"` // import / modify / delete / manual_sync / restore
	Tag       string          `json:"tag"`     // manual / auto
	Payload   json.RawMessage `json:"payload"` // 整库快照（恢复数据源）
	CreatedAt time.Time       `json:"created_at"`
}

// BookSnapshotItem 整库快照中的单本书（uuid + 书籍 payload）。
type BookSnapshotItem struct {
	UUID        string          `json:"uuid"`
	Name        string          `json:"name"`
	CurrentPage int             `json:"current_page"`
	CoverHash   string          `json:"cover_hash"`
	Files       json.RawMessage `json:"files"`
}

// BookRestoreResult 归档恢复结果。
type BookRestoreResult struct {
	Restored int   `json:"restored"` // 恢复的书籍数
	Revision int64 `json:"revision"` // 本次恢复推进的最大 revision
}
