package api

import (
	"github.com/gin-gonic/gin"

	"TelebookServer/internal/config"
	"TelebookServer/internal/service"
	"TelebookServer/internal/store"
)

// Dependencies 路由依赖集合。
type Dependencies struct {
	Config  *config.Config
	Auth    *service.AuthService
	JWT     *service.JWTService
	Devices store.DeviceStore
	Sync    *service.SyncService
	Files   *service.FileService   // 未配置 MinIO 时为 nil，文件接口返回 503
	Upload  *service.UploadService // 书籍上传任务（§2.1.3；nil 则上传接口返回 503）
}

// NewRouter 组装路由。
func NewRouter(deps *Dependencies) *gin.Engine {
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())

	// 公共：健康检查 / 连接测试
	r.GET("/ping", PingHandler(deps.Config))

	// API v1
	api := r.Group("/api/v1")
	{
		// 公共：设备注册（密钥换 JWT）
		api.POST("/auth/register", RegisterHandler(deps.Auth))
		api.POST("/auth/refresh", RefreshHandler(deps.Auth))

		// 需要 JWT
		authed := api.Group("")
		authed.Use(AuthMiddleware(deps.JWT, deps.Devices))
		{
			authed.GET("/devices/me", MeHandler(deps.Devices))
			authed.POST("/sync/push", PushHandler(deps.Sync))
			authed.GET("/sync/pull", PullHandler(deps.Sync))
			authed.GET("/sync/status", SyncStatusHandler(deps.Sync))
			authed.GET("/sync/library", LibraryStatusHandler(deps.Sync))
			authed.GET("/sync/books", LibraryBooksHandler(deps.Sync))
			authed.GET("/books/history", ListHistoryHandler(deps.Sync))
			authed.POST("/books/history", RecordHistoryHandler(deps.Sync))
			authed.POST("/books/restore", RestoreBookHandler(deps.Sync))
			authed.GET("/conflicts", ListConflictsHandler(deps.Sync))
			authed.POST("/conflicts/:id/resolve", ResolveConflictHandler(deps.Sync))

			if deps.Files != nil {
				authed.POST("/files/check", CheckFilesHandler(deps.Files))
				authed.POST("/files/upload/direct", FileDirectUploadHandler(deps.Files))
				authed.POST("/files/upload/init", FileInitUploadHandler(deps.Files))
				authed.POST("/files/upload", FileUploadPartHandler(deps.Files))
				authed.POST("/files/upload/complete", FileCompleteUploadHandler(deps.Files))
				authed.GET("/files/download", FileDownloadHandler(deps.Files))
			} else {
				authed.Any("/files/*path", FilesNotConfiguredHandler())
			}

			// 书籍上传任务（§2.1.3：init/status/complete + 单文件 done；abandon 放弃清理）
			if deps.Upload != nil {
				authed.POST("/upload/init", UploadInitHandler(deps.Upload))
				authed.GET("/upload/status/:uuid", UploadStatusHandler(deps.Upload))
				authed.POST("/upload/file/:uuid/done", UploadFileDoneHandler(deps.Upload))
				authed.POST("/upload/complete/:uuid", UploadCompleteHandler(deps.Upload))
				authed.POST("/upload/abandon/:uuid", UploadAbandonHandler(deps.Upload))
			} else {
				authed.Any("/upload/*path", FilesNotConfiguredHandler())
			}
		}
	}
	return r
}

// FilesNotConfiguredHandler 未配置 MinIO 时的占位。
func FilesNotConfiguredHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		respondError(c, 503, "files_not_configured", "file sync is not configured on server")
	}
}
