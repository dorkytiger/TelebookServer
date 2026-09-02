package api

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"

	"TelebookServer/internal/model"
	"TelebookServer/internal/service"
)

// UploadInitHandler init 上传任务：客户端上报书清单，服务器分配 uuid + 返回待上传清单。
func UploadInitHandler(upload *service.UploadService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req model.UploadInitRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid request body")
			return
		}
		if len(req.Books) == 0 {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "books is empty")
			return
		}
		deviceID := c.GetString(ctxDeviceID)
		resp, err := upload.InitUpload(c.Request.Context(), deviceID, req)
		if err != nil {
			log.Printf("upload init failed: %v", err)
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// UploadStatusHandler 断点续传查询。
func UploadStatusHandler(upload *service.UploadService) gin.HandlerFunc {
	return func(c *gin.Context) {
		uuid := c.Param("uuid")
		if uuid == "" {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "uuid is required")
			return
		}
		resp, err := upload.Status(c.Request.Context(), uuid)
		if err != nil {
			if errors.Is(err, service.ErrUploadNotFound) {
				respondError(c, http.StatusNotFound, "not_found", "upload task not found")
				return
			}
			log.Printf("upload status failed: %v", err)
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// UploadFileDoneHandler 标记单文件上传完成。
func UploadFileDoneHandler(upload *service.UploadService) gin.HandlerFunc {
	return func(c *gin.Context) {
		uuid := c.Param("uuid")
		hash := c.Query("hash")
		if uuid == "" || hash == "" {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "uuid and hash are required")
			return
		}
		if err := upload.MarkFileDone(c.Request.Context(), uuid, hash); err != nil {
			log.Printf("upload file done failed: %v", err)
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, gin.H{"done": true})
	}
}

// UploadCompleteHandler 整本完成：服务器校验全部文件后落库 current_book。
func UploadCompleteHandler(upload *service.UploadService) gin.HandlerFunc {
	return func(c *gin.Context) {
		uuid := c.Param("uuid")
		if uuid == "" {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "uuid is required")
			return
		}
		deviceID := c.GetString(ctxDeviceID)
		resp, err := upload.Complete(c.Request.Context(), deviceID, uuid)
		if err != nil {
			if errors.Is(err, service.ErrUploadNotFound) {
				respondError(c, http.StatusNotFound, "not_found", "upload task not found")
				return
			}
			log.Printf("upload complete failed: %v", err)
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// UploadAbandonHandler 放弃上传（客户端覆盖/删除本地书后调用，§8.2 清理）：
// 标记任务 failed，孤儿任务不再参与后续 complete/断点查询。幂等（不存在也返回 ok）。
func UploadAbandonHandler(upload *service.UploadService) gin.HandlerFunc {
	return func(c *gin.Context) {
		uuid := c.Param("uuid")
		if uuid == "" {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "uuid is required")
			return
		}
		if err := upload.MarkFailed(c.Request.Context(), uuid); err != nil {
			log.Printf("upload abandon failed: %v", err)
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, gin.H{"done": true})
	}
}
