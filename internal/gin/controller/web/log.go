package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"github.com/nichuanfang/gymdl/utils"
)

var validLevels = map[string]struct{}{
	"debug": {}, "info": {}, "warn": {}, "error": {},
}

// HandleListLogs GET /api/web/logs?after=0&level=info&limit=300
// 以短轮询 + 单调递增游标提供日志，浏览器不需要维持易受代理影响的 SSE 长连接。
func HandleListLogs(c *gin.Context) {
	level := strings.ToLower(strings.TrimSpace(c.DefaultQuery("level", "info")))
	if _, ok := validLevels[level]; !ok {
		level = "info"
	}

	after, err := strconv.ParseUint(c.DefaultQuery("after", "0"), 10, 64)
	if err != nil {
		after = 0
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "300"))
	if err != nil || limit < 1 {
		limit = 300
	}
	if limit > 500 {
		limit = 500
	}

	response.Success(c, utils.ReadLogs(after, level, limit))
}

// HandleLogLevels GET /api/web/logs/levels
func HandleLogLevels(c *gin.Context) {
	response.Success(c, gin.H{"levels": []string{"debug", "info", "warn", "error"}})
}

// HandleTruncateLog DELETE /api/web/logs — 仅预留，不做文件清空操作
func HandleTruncateLog(c *gin.Context) {
	response.Fail(c, http.StatusNotImplemented, "暂不支持清空日志文件")
}
