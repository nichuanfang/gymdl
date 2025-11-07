package controller

import (
	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/gin/response"
)

// SyncCookieCloud 同步cookie
func SyncCookieCloud(c *gin.Context) {
	core.GlobalCookieCloud.Sync()
	response.Success(c, "cookiecloud sync success")
}
