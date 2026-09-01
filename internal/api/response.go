package api

import "github.com/gin-gonic/gin"

// respondError 统一错误结构：{ "error": { "code": "...", "message": "..." } }
func respondError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    code,
			"message": message,
		},
	})
}
