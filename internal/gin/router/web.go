package router

import (
	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/gin/middleware"
	"github.com/nichuanfang/gymdl/internal/gin/task"
)

// SetupWebRoutes 注册 WebUI 相关路由
func SetupWebRoutes(rg *gin.RouterGroup, c *config.Config) {
	SetupWebRoutesWithTaskManager(rg, c, task.NewTaskManager(c))
}

// SetupWebRoutesWithTaskManager registers WebUI routes with an injected task
// manager. Tests use this to keep SQLite state inside their temporary directory.
func SetupWebRoutesWithTaskManager(rg *gin.RouterGroup, c *config.Config, tm *task.TaskManager) {
	// 注入配置
	web.SetWebConfig(c)
	web.SetConfigFilePath(c.ConfigFile)

	web.SetTaskManager(tm)
	// The one-time Telegram WebApp selection token and signed initData protect
	// this callback; it must remain outside the WebUI's cookie-auth group.
	rg.POST("/playlist-assignment/:token", web.HandlePlaylistAssignmentWebAppSelection)

	group := rg.Group("/web")
	authSecure := c.WebConfig != nil && c.WebConfig.Https
	webAuth := middleware.NewWebAuth(authSecure, c.WebConfig.Auth)
	group.Use(webAuth.Middleware())
	group.POST("/auth/login", webAuth.Login)
	group.GET("/auth/session", webAuth.Session)
	group.POST("/auth/logout", webAuth.Logout)

	// 系统状态与配置
	group.GET("/system/status", web.HandleSystemStatus)
	group.GET("/dashboard/summary", web.HandleDashboardSummary)
	group.GET("/webdav/pending", web.HandlePendingUploads)
	group.GET("/listening/report", web.HandleListeningReport)
	group.GET("/system/config", web.HandleSystemConfig)
	group.PUT("/system/config", web.HandleUpdateConfig)
	group.POST("/system/restart", web.HandleRestart)

	// 任务
	group.POST("/task/submit", web.HandleSubmitTask)
	group.GET("/task/active", web.HandleActiveTasks)
	group.GET("/task/:id/events", web.HandleTaskEvents)
	group.DELETE("/task/:id", web.HandleCancelTask)
	group.GET("/task/history", web.HandleTaskHistory)

	// CookieCloud
	group.GET("/cookiecloud/status", web.HandleCookieCloudStatus)
	group.POST("/cookiecloud/sync", web.HandleCookieCloudSync)

	// QQ 音乐扫码登录
	group.GET("/qqmusic/status", web.HandleQQLoginStatusOverview)
	group.POST("/qqmusic/qrcode", web.HandleQQLoginStart)
	group.GET("/qqmusic/qrcode/:identifier", web.HandleQQLoginStatus)
	group.DELETE("/qqmusic/qrcode/:identifier", web.HandleQQLoginCancel)

	// 搜索
	group.GET("/search", web.HandleSearch)
	group.GET("/search/stream", web.HandleSearchStream)

	// 日志
	group.GET("/logs", web.HandleListLogs)
	group.GET("/logs/levels", web.HandleLogLevels)

	// 已注册的定时任务。只查看和立即执行，不改时间表。
	group.GET("/cron/jobs", web.HandleCronJobs)
	group.POST("/cron/jobs/:name/run", web.HandleRunCronJob)

	// 歌单描述、找回遗珠、AI 歌单。确认前不会写入 Navidrome。
	group.GET("/playlists", web.HandlePlaylistDescriptions)
	group.PUT("/playlists/:id/desc", web.HandleUpdatePlaylistDescription)
	group.POST("/playlists/sync", web.HandleSyncPlaylists)
	group.POST("/discovery/rediscover", web.HandleRediscover)
	group.POST("/discovery/enqueue", web.HandleRediscoverEnqueue)
	group.POST("/discovery/dismiss", web.HandleRediscoverDismiss)
	group.GET("/ai/playlists", web.HandleAIPlaylists)
	group.POST("/ai/playlists/preview", web.HandleAIPlaylistPreview)
	group.POST("/ai/playlists/confirm", web.HandleAIPlaylistConfirm)

	// 查重本身不删除。只有选定留下的版本后，才会删同组里勾选的文件。
	group.GET("/duplicates", web.HandleLibraryDuplicates)
	group.POST("/duplicates/scan", web.HandleStartLibraryDuplicateScan)
	group.POST("/duplicates/keep", web.HandleKeepLibraryDuplicate)

	// 文件
	group.GET("/files", web.HandleListFiles)
	group.GET("/files/stream", web.HandleStreamFile)
	group.DELETE("/files", web.HandleDeleteFile)
}
