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
	// 注入配置
	web.SetWebConfig(c)
	web.SetConfigFilePath(c.ConfigFile)

	// 初始化任务管理器
	tm := task.NewTaskManager(c)
	web.SetTaskManager(tm)

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

	// 文件
	group.GET("/files", web.HandleListFiles)
	group.GET("/files/stream", web.HandleStreamFile)
	group.DELETE("/files", web.HandleDeleteFile)
}
