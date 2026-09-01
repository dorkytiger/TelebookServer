package api

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"TelebookServer/internal/model"
	"TelebookServer/internal/service"
	"TelebookServer/internal/store"
)

// ListConflictsHandler 未解决冲突列表。
func ListConflictsHandler(sync *service.SyncService) gin.HandlerFunc {
	return func(c *gin.Context) {
		conflicts, err := sync.ListConflicts(c.Request.Context())
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		if conflicts == nil {
			conflicts = []model.Conflict{}
		}
		c.JSON(http.StatusOK, gin.H{"conflicts": conflicts})
	}
}

// ResolveConflictHandler 解决冲突（keep_local / keep_server / manual）。
func ResolveConflictHandler(sync *service.SyncService) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID := c.GetString(ctxDeviceID)

		conflictID, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil || conflictID < 1 {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid conflict id")
			return
		}

		var req model.ResolveRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid request body")
			return
		}

		if err := sync.ResolveConflict(c.Request.Context(), deviceID, conflictID, req); err != nil {
			switch {
			case errors.Is(err, service.ErrConflictNotFound):
				respondError(c, http.StatusNotFound, "not_found", "conflict not found")
			case errors.Is(err, service.ErrConflictAlreadyResolved):
				respondError(c, http.StatusConflict, "conflict_resolved", "conflict already resolved")
			case errors.Is(err, service.ErrInvalidStrategy), err != nil && len(req.Payload) == 0:
				respondError(c, http.StatusUnprocessableEntity, "validation_error", err.Error())
			default:
				respondError(c, http.StatusInternalServerError, "internal", "internal error")
			}
			return
		}
		c.JSON(http.StatusOK, gin.H{"resolved": true})
	}
}

// CheckFilesHandler 批量 hash 比对。
func CheckFilesHandler(files *service.FileService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.FileCheckRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid request body")
			return
		}
		if len(req.Files) == 0 {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "files is empty")
			return
		}
		for _, f := range req.Files {
			if f.Hash == "" {
				respondError(c, http.StatusUnprocessableEntity, "validation_error", "hash is required")
				return
			}
		}

		resp, err := files.CheckFiles(c.Request.Context(), req.Files)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// FileInitUploadHandler 初始化分片上传。
func FileInitUploadHandler(files *service.FileService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.FileInitUploadRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid request body")
			return
		}
		resp, err := files.InitUpload(c.Request.Context(), req.Hash, req.Size)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// FileUploadPartHandler 上传一个分片（multipart: hash, upload_id, part_number, chunk）。
func FileUploadPartHandler(files *service.FileService) gin.HandlerFunc {
	return func(c *gin.Context) {
		hash := c.PostForm("hash")
		uploadID := c.PostForm("upload_id")
		partNumber, err := strconv.Atoi(c.PostForm("part_number"))
		if err != nil || partNumber < 1 {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid part_number")
			return
		}

		fileHeader, err := c.FormFile("chunk")
		if err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "chunk is required")
			return
		}
		src, err := fileHeader.Open()
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		defer src.Close()

		data, err := io.ReadAll(src)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}

		etag, err := files.UploadPart(c.Request.Context(), hash, uploadID, partNumber, data)
		if err != nil {
			if errors.Is(err, service.ErrFileExists) {
				// 文件已存在（另一设备已传完）：幂等视为成功
				c.JSON(http.StatusOK, gin.H{"hash": hash, "complete": true})
				return
			}
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, gin.H{"hash": hash, "part_number": partNumber, "etag": etag})
	}
}

// FileCompleteUploadHandler 完成分片上传。
func FileCompleteUploadHandler(files *service.FileService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			model.FileCompleteUploadRequest
			Parts []model.FilePartMeta `json:"parts"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid request body")
			return
		}
		if req.TotalParts < 1 || len(req.Parts) != req.TotalParts {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "parts must match total_parts")
			return
		}

		parts := make([]store.UploadPartMeta, 0, len(req.Parts))
		for _, p := range req.Parts {
			if p.PartNumber < 1 || p.ETag == "" {
				respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid part")
				return
			}
			parts = append(parts, store.UploadPartMeta{PartNumber: p.PartNumber, ETag: p.ETag})
		}

		if err := files.CompleteUpload(c.Request.Context(), req.Hash, req.UploadID, req.Size, parts); err != nil {
			if errors.Is(err, service.ErrUploadNotComplete) {
				respondError(c, http.StatusUnprocessableEntity, "validation_error", "upload parts incomplete")
				return
			}
			log.Printf("complete upload error: %v", err)
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, gin.H{"hash": req.Hash, "complete": true})
	}
}

// FileDownloadHandler 生成预签名下载地址并 302 跳转。
// 预签名 URL 的 host 从请求推断（X-Forwarded-Host 优先），
// 保证手机拿到的地址与访问 API 的地址一致。
func FileDownloadHandler(files *service.FileService) gin.HandlerFunc {
	return func(c *gin.Context) {
		hash := c.Query("hash")
		if hash == "" {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "hash is required")
			return
		}
		url, err := files.PresignDownload(c.Request.Context(), hash, requestHost(c.Request))
		if err != nil {
			if errors.Is(err, store.ErrObjectNotFound) {
				respondError(c, http.StatusNotFound, "not_found", "file not found")
				return
			}
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.Redirect(http.StatusFound, url)
	}
}

// requestHost 返回客户端可达的主机名：优先 X-Forwarded-Host（反代场景），
// 否则用请求 Host（去掉端口）。用于预签名 URL 自动推断 MinIO 公网地址。
func requestHost(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	// 去掉端口：预签名拼接端口用 MINIO_PUBLIC_PORT
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}
