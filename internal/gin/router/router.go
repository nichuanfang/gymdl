package router

import (
	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/gin/middleware"
	"github.com/nichuanfang/gymdl/utils"
	"go.uber.org/zap"
)

var logger *zap.Logger

// SetupRouter 路由注册
func SetupRouter(c *config.Config) *gin.Engine {
	// 日志初始化
	logger = utils.Logger()
	engine := gin.New()

	// 设置 Gin 的输出遵循 zap 配置
	gin.DefaultWriter = zap.NewStdLog(utils.Logger()).Writer()
	gin.DefaultErrorWriter = zap.NewStdLog(utils.Logger()).Writer()

	// 中间件注册(扩展) 常见中间件有日志、鉴权、限流、跨域等
	engine.Use(middleware.GinLoggerMiddleware(), middleware.GinRecoveryMiddleware())

	// 基础路由组
	baseGroup := engine.Group("/")
	RegisterBaseRoutes(baseGroup)
	// api路由组
	apiGroup := engine.Group("/api")
	// 注册文本处理器路由
	RegisterTextRoutes(apiGroup)
	// 注册指令处理器路由
	RegisterCommandRoutes(apiGroup)
	return engine
}
