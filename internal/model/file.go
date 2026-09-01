package model

// FileCheckItem 文件指纹（hash + size）。
type FileCheckItem struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// FileCheckRequest /files/check 请求。
type FileCheckRequest struct {
	Files []FileCheckItem `json:"files"`
}

// FileCheckResponse /files/check 响应：远端缺失清单。
type FileCheckResponse struct {
	Missing []FileCheckItem `json:"missing"`
}

// FileInitUploadRequest 分片上传初始化。
type FileInitUploadRequest struct {
	Hash string `json:"hash" binding:"required"`
	Size int64  `json:"size" binding:"required"`
}

// FileInitUploadResponse 返回 upload_id；complete=true 表示文件已存在（幂等）。
type FileInitUploadResponse struct {
	UploadID string `json:"upload_id,omitempty"`
	Complete bool   `json:"complete"`
}

// FileCompleteUploadRequest 完成分片上传。
type FileCompleteUploadRequest struct {
	Hash       string `json:"hash" binding:"required"`
	UploadID   string `json:"upload_id" binding:"required"`
	Size       int64  `json:"size" binding:"required"`
	TotalParts int    `json:"total_parts" binding:"required"`
}

// FilePartMeta 已上传分片（partNumber + ETag）。
type FilePartMeta struct {
	PartNumber int    `json:"part_number"`
	ETag       string `json:"etag"`
}
