package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"TelebookServer/internal/config"
	"TelebookServer/internal/model"
	"TelebookServer/internal/service"
	"TelebookServer/internal/store"
)

// PingHandler 健康检查 / 连接测试（无需鉴权）。
func PingHandler(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status":      "ok",
			"server_time": time.Now().UTC().Format(time.RFC3339),
			"version":     cfg.Version,
		})
	}
}

// RegisterHandler 设备注册：密钥换 JWT。
func RegisterHandler(auth *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req service.RegisterRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid request body")
			return
		}

		resp, err := auth.Register(c.Request.Context(), req)
		if err != nil {
			switch {
			case errors.Is(err, service.ErrInvalidKey):
				respondError(c, http.StatusUnauthorized, "invalid_connection_key", "invalid connection key")
			case errors.Is(err, service.ErrDeviceConflict):
				respondError(c, http.StatusConflict, "device_conflict", "device already registered")
			default:
				respondError(c, http.StatusInternalServerError, "internal", "internal error")
			}
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// RefreshHandler 用 refresh token 换新 access token（并轮换 refresh token）。
func RefreshHandler(auth *service.AuthService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req service.RefreshRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid request body")
			return
		}
		resp, err := auth.Refresh(c.Request.Context(), req)
		if err != nil {
			switch {
			case errors.Is(err, service.ErrInvalidRefreshToken):
				respondError(c, http.StatusUnauthorized, "invalid_refresh_token", "invalid or expired refresh token")
			default:
				respondError(c, http.StatusInternalServerError, "internal", "internal error")
			}
			return
		}
		c.JSON(http.StatusOK, resp)
	}
}

// MeHandler 返回当前 token 对应的设备信息。
func MeHandler(devices store.DeviceStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID := c.GetString(ctxDeviceID)
		d, err := devices.GetDeviceByID(c.Request.Context(), deviceID)
		if err != nil {
			if errors.Is(err, store.ErrDeviceNotFound) {
				respondError(c, http.StatusNotFound, "not_found", "device not found")
				return
			}
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"device_id":    d.ID,
			"device_name":  d.Name,
			"platform":     d.Platform,
			"created_at":   d.CreatedAt,
			"last_seen_at": d.LastSeenAt,
		})
	}
}

// PushHandler 批量提交本地变更。
func PushHandler(sync *service.SyncService) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID := c.GetString(ctxDeviceID)

		var req struct {
			Source  string         `json:"source"` // manual | auto（缺省 auto）
			Changes []model.Change `json:"changes" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid request body")
			return
		}
		if req.Source == "" {
			req.Source = model.SyncSourceAuto
		}
		if req.Source != model.SyncSourceAuto && req.Source != model.SyncSourceManual {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "source must be manual or auto")
			return
		}
		if len(req.Changes) == 0 {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "changes is empty")
			return
		}
		for _, ch := range req.Changes {
			if err := validateChange(ch); err != nil {
				respondError(c, http.StatusUnprocessableEntity, "validation_error", err.Error())
				return
			}
		}

		results, err := sync.Push(c.Request.Context(), deviceID, req.Source, req.Changes)
		if err != nil {
			log.Printf("push error: %v", err)
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, gin.H{"results": results})
	}
}

// PullHandler 增量拉取：cursor 之后的事件。
func PullHandler(sync *service.SyncService) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID := c.GetString(ctxDeviceID)

		cursor, err := strconv.ParseInt(c.DefaultQuery("cursor", "0"), 10, 64)
		if err != nil || cursor < 0 {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid cursor")
			return
		}
		limit, err := strconv.ParseInt(c.DefaultQuery("limit", "500"), 10, 64)
		if err != nil || limit < 1 {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid limit")
			return
		}
		if limit > 1000 {
			limit = 1000
		}

		result, err := sync.Pull(c.Request.Context(), deviceID, cursor, limit)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, result)
	}
}

// SyncStatusHandler 返回当前同步状态。
func SyncStatusHandler(sync *service.SyncService) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID := c.GetString(ctxDeviceID)
		status, err := sync.Status(c.Request.Context(), deviceID)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, status)
	}
}

func validateChange(ch model.Change) error {
	if ch.ChangeID == "" {
		return errors.New("change_id is required")
	}
	if ch.EntityType == "" {
		return errors.New("entity_type is required")
	}
	if ch.EntityID == "" {
		return errors.New("entity_id is required")
	}
	if ch.Op != model.OpUpsert && ch.Op != model.OpDelete {
		return errors.New("op must be upsert or delete")
	}
	return nil
}

// ListHistoryHandler 归档历史列表（整库快照）：GET /books/history?limit=
func ListHistoryHandler(sync *service.SyncService) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, err := strconv.Atoi(c.DefaultQuery("limit", "200"))
		if err != nil || limit < 1 || limit > 1000 {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "invalid limit")
			return
		}
		history, err := sync.ListHistory(c.Request.Context(), limit)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, gin.H{"history": history})
	}
}

// RecordHistoryHandler 客户端驱动记录整库快照：POST /books/history {op_type, tag, snapshot}
func RecordHistoryHandler(sync *service.SyncService) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			OpType   string          `json:"op_type" binding:"required"`
			Tag      string          `json:"tag" binding:"required"`
			Snapshot json.RawMessage `json:"snapshot" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "op_type/tag/snapshot are required")
			return
		}
		id, err := sync.RecordHistory(c.Request.Context(), req.OpType, req.Tag, req.Snapshot)
		if err != nil {
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			return
		}
		c.JSON(http.StatusOK, gin.H{"id": id})
	}
}

// RestoreBookHandler 整库恢复：POST /books/restore {history_id}
func RestoreBookHandler(sync *service.SyncService) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID := c.GetString(ctxDeviceID)

		var req struct {
			HistoryID int64 `json:"history_id" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			respondError(c, http.StatusUnprocessableEntity, "validation_error", "history_id is required")
			return
		}

		result, err := sync.RestoreBook(c.Request.Context(), deviceID, req.HistoryID)
		if err != nil {
			switch {
			case errors.Is(err, store.ErrHistoryNotFound):
				respondError(c, http.StatusNotFound, "not_found", "history entry not found")
			default:
				respondError(c, http.StatusInternalServerError, "internal", "internal error")
			}
			return
		}
		c.JSON(http.StatusOK, result)
	}
}
