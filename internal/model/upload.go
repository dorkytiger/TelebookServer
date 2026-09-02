package model

import "time"

// ── init：客户端上报书清单，服务器分配 uuid ──

// UploadInitRequest init 上传：一次会话可含多本书。
type UploadInitRequest struct {
	Books []UploadInitBook `json:"books"`
}

// UploadInitBook 单本书的 init 数据。
type UploadInitBook struct {
	UUID        string         `json:"uuid"`      // 客户端本地 uuid（§6 方案1：保留）；空则服务器分配
	ClientID    string         `json:"client_id"` // 客户端临时 id（回包对应）
	Name        string         `json:"name"`
	Files       []BookFileMeta `json:"files"`
	DataVersion string         `json:"data_version,omitempty"` // 客户端数据版本（并发匹配）
}

// UploadInitResponse init 返回：每本书分配的 uuid + 待上传清单。
type UploadInitResponse struct {
	Books []UploadInitBookResult `json:"books"`
}

// UploadInitBookResult 单本书的 init 结果。
type UploadInitBookResult struct {
	ClientID     string         `json:"client_id"`
	UUID         string         `json:"uuid"`
	TotalFiles   int            `json:"total_files"`
	PendingFiles []BookFileMeta `json:"pending_files"` // 待上传（已 done 的剔除）
}

// ── status：断点续传查询 ──

// UploadStatusResponse 断点续传查询。
type UploadStatusResponse struct {
	UUID         string         `json:"uuid"`
	Name         string         `json:"name"`
	Status       string         `json:"status"` // uploading / done / failed
	TotalFiles   int            `json:"total_files"`
	DoneFiles    int            `json:"done_files"`
	PendingFiles []BookFileMeta `json:"pending_files"`
}

// ── complete：整本完成，服务器校验后落库 ──

// UploadCompleteRequest 完成上传（整本）。
type UploadCompleteRequest struct {
	UUID string `json:"uuid"`
}

// UploadCompleteResponse complete 结果。
type UploadCompleteResponse struct {
	UUID   string `json:"uuid"`
	Done   bool   `json:"done"`
	Reason string `json:"reason,omitempty"` // ok / incomplete / hash_mismatch / not_found
	// Revision 整本落库后服务器 current_book 的版本号
	// （客户端回填 sync_state 作乐观锁基准，§2.1.5）。
	Revision int64 `json:"revision"`
}

// ── 服务端内部上传任务记录 ──

// UploadBookTask 服务端上传任务（单本书）。
type UploadBookTask struct {
	UUID        string    `json:"uuid"`
	Name        string    `json:"name"`
	Status      string    `json:"status"` // uploading / done / failed
	TotalFiles  int       `json:"total_files"`
	DoneFiles   int       `json:"done_files"`
	DataVersion string    `json:"data_version"`
	DeviceID    string    `json:"device_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
