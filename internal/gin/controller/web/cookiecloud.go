package web

import (
	"os"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/gin/response"
)

// CookieCloudStatusResponse 同步状态
type CookieCloudStatusResponse struct {
	Available   bool   `json:"available"`
	Mode        int    `json:"mode"`
	ExpireMin   int    `json:"expire_min"`
	CookiePath  string `json:"cookie_path"`
	FileExists  bool   `json:"file_exists"`
	FileModTime string `json:"file_mod_time,omitempty"`
}

// HandleCookieCloudStatus GET /api/web/cookiecloud/status
func HandleCookieCloudStatus(c *gin.Context) {
	var cookieConfig *config.CookieCloudConfig
	if cfg := GetWebConfig(); cfg != nil {
		cookieConfig = cfg.CookieCloud
	}
	resp := CookieCloudStatusResponse{}
	if cookieConfig != nil {
		resp.Mode = cookieConfig.Mode
		resp.ExpireMin = cookieConfig.ExpireTime
	}

	if cookieConfig != nil && cookieConfig.CookieFilePath != "" && cookieConfig.CookieFile != "" {
		cookiePath := filepath.Join(cookieConfig.CookieFilePath, cookieConfig.CookieFile)
		resp.CookiePath = cookiePath
		if info, err := os.Stat(cookiePath); err == nil {
			resp.FileExists = true
			resp.FileModTime = info.ModTime().Format(time.RFC3339)
		}
	}

	if core.GlobalCookieCloud != nil {
		resp.Available = core.GlobalCookieCloud.CheckConnection()
	}
	response.Success(c, resp)
}

// HandleCookieCloudSync POST /api/web/cookiecloud/sync
func HandleCookieCloudSync(c *gin.Context) {
	if core.GlobalCookieCloud == nil {
		response.Fail(c, 400, "CookieCloud 未初始化")
		return
	}
	go core.GlobalCookieCloud.Sync()
	response.Success(c, gin.H{"triggered": true})
}
