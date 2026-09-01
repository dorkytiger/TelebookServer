package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"TelebookServer/internal/service"
	"TelebookServer/internal/store"
)

const ctxDeviceID = "device_id"

// AuthMiddleware 校验 Bearer JWT，成功后注入 device_id 到上下文。
//
// 同时校验设备仍注册在库：服务器数据重置后旧 token 返回 401（而非 500 外键错误），
// 客户端据此重新注册。
func AuthMiddleware(jwt *service.JWTService, devices store.DeviceStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			respondError(c, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			c.Abort()
			return
		}
		claims, err := jwt.Parse(strings.TrimPrefix(header, "Bearer "))
		if err != nil {
			respondError(c, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
			c.Abort()
			return
		}
		if _, err := devices.GetDeviceByID(c.Request.Context(), claims.DeviceID); err != nil {
			if errors.Is(err, store.ErrDeviceNotFound) {
				respondError(c, http.StatusUnauthorized, "unauthorized", "device not registered")
				c.Abort()
				return
			}
			respondError(c, http.StatusInternalServerError, "internal", "internal error")
			c.Abort()
			return
		}
		c.Set(ctxDeviceID, claims.DeviceID)
		c.Next()
	}
}
