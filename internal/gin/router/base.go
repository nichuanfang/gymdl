package router

import (
	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/gin/controller"
)

func RegisterBaseRoutes(rg *gin.RouterGroup) {
	rg.GET("/", controller.HelloWorld)
	// cookiecloud同步
	rg.POST("/cookiecloud", controller.SyncCookieCloud)
}
