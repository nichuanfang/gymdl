package web

import (
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/internal/gin/response"
)

// restartSupported is intentionally limited to containers whose restart policy
// can bring the process back after graceful shutdown.
func restartSupported() bool {
	enabled, _ := strconv.ParseBool(os.Getenv("GYMDL_WEBUI_RESTART_ENABLED"))
	if !enabled {
		return false
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	return false
}

// HandleRestart requests a graceful process exit. Docker's configured restart
// policy starts the container again; unmanaged local processes are not stopped.
func HandleRestart(c *gin.Context) {
	if !restartSupported() {
		response.Fail(c, http.StatusNotImplemented, "应用重启仅支持配置了重启策略的容器环境")
		return
	}
	if manager != nil {
		active := manager.GetActiveTasks()
		if len(active) > 0 {
			response.Fail(c, http.StatusConflict, "仍有下载任务运行或排队，请先等待任务结束后再重启")
			return
		}
	}

	response.Success(c, gin.H{"restarting": true})
	go func() {
		time.Sleep(800 * time.Millisecond)
		process, err := os.FindProcess(os.Getpid())
		if err == nil {
			_ = process.Signal(os.Interrupt)
		}
	}()
}
