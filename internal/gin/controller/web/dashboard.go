package web

import (
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/internal/gin/response"
	"github.com/nichuanfang/gymdl/internal/gin/task"
	"github.com/nichuanfang/gymdl/utils"
)

type ServiceStatus struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Online  bool   `json:"online"`
	Detail  string `json:"detail"`
}

type SystemStatusResponse struct {
	Services         []ServiceStatus       `json:"services"`
	Version          string                `json:"version"`
	GoVer            string                `json:"go_version"`
	Uptime           int64                 `json:"uptime"`
	CheckedAt        *time.Time            `json:"checked_at,omitempty"`
	RestartSupported bool                  `json:"restart_supported"`
	Metrics          task.DashboardMetrics `json:"metrics,omitempty"`
}

var (
	startTime        = time.Now()
	statusCacheMu    sync.RWMutex
	statusCache      []ServiceStatus
	statusCheckedAt  time.Time
	statusRefreshing bool
)

// HandleSystemStatus GET /api/web/system/status keeps the original system summary API.
func HandleSystemStatus(c *gin.Context) {
	response.Success(c, getSystemStatus(false))
}

// HandleDashboardSummary GET /api/web/dashboard/summary returns cached checks and task counters.
func HandleDashboardSummary(c *gin.Context) {
	response.Success(c, getSystemStatus(true))
}

func getSystemStatus(withMetrics bool) SystemStatusResponse {
	cfg := GetWebConfig()
	if cfg != nil {
		statusCacheMu.Lock()
		if time.Since(statusCheckedAt) > 30*time.Second && !statusRefreshing {
			statusRefreshing = true
			go refreshServiceStatus(cfg)
		}
		statusCacheMu.Unlock()
	}

	statusCacheMu.RLock()
	services := append([]ServiceStatus(nil), statusCache...)
	checkedAt := checkedAtOrNil(statusCheckedAt)
	statusCacheMu.RUnlock()
	if len(services) == 0 && cfg != nil {
		services = initialServiceStatuses(cfg)
	}
	out := SystemStatusResponse{
		Services:         services,
		Version:          "dev-main",
		GoVer:            runtime.Version(),
		Uptime:           int64(time.Since(startTime).Seconds()),
		CheckedAt:        checkedAt,
		RestartSupported: restartSupported(),
	}
	if withMetrics && manager != nil {
		out.Metrics = manager.DashboardMetrics(time.Now())
	}
	return out
}

func checkedAtOrNil(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	checkedAt := value
	return &checkedAt
}

func initialServiceStatuses(cfg *config.Config) []ServiceStatus {
	services := make([]ServiceStatus, 0, 8)
	add := func(name string, enabled bool, detail string) {
		if enabled && detail == "" {
			detail = "检测中"
		}
		if !enabled {
			detail = "未启用"
		}
		services = append(services, ServiceStatus{Name: name, Enabled: enabled, Online: false, Detail: detail})
	}
	add("WebDAV", cfg.Tidy != nil && cfg.Tidy.Mode == 2, "")
	add("CookieCloud", cfg.CookieCloud != nil && cfg.CookieCloud.CookieCloudUrl != "", "")
	add("LrcAPI", cfg.LrcAPI != nil && cfg.LrcAPI.Enable, "")
	add("AI", cfg.AI != nil && cfg.AI.Enable, "")
	add("QQMusicApi", cfg.QQMusicApiConfig != nil && cfg.QQMusicApiConfig.Enable, "")
	add("n8n", cfg.N8NConfig != nil && cfg.N8NConfig.Enable, "")
	monitorEnabled := cfg.AdditionalConfig != nil && cfg.AdditionalConfig.EnableDirMonitor
	monitorCount := 0
	if cfg.AdditionalConfig != nil {
		monitorCount = len(cfg.AdditionalConfig.MonitorDirs)
	}
	services = append(services, ServiceStatus{Name: "目录监听", Enabled: monitorEnabled, Online: monitorEnabled, Detail: fmt.Sprintf("%d 个目录", monitorCount)})
	cronEnabled := cfg.AdditionalConfig != nil && cfg.AdditionalConfig.EnableCron
	services = append(services, ServiceStatus{Name: "定时任务", Enabled: cronEnabled, Online: cronEnabled})
	return services
}

func refreshServiceStatus(cfg *config.Config) {
	defer func() {
		statusCacheMu.Lock()
		statusRefreshing = false
		statusCacheMu.Unlock()
	}()

	services := initialServiceStatuses(cfg)
	type probeResult struct {
		index int
		ok    bool
	}
	probes := make([]func() bool, len(services))
	for i := range services {
		if !services[i].Enabled {
			continue
		}
		switch services[i].Name {
		case "WebDAV":
			probes[i] = func() bool { return core.GlobalWebDAV != nil && core.GlobalWebDAV.CheckConnection() }
		case "CookieCloud":
			probes[i] = func() bool { return core.GlobalCookieCloud != nil && core.GlobalCookieCloud.CheckConnection() }
		case "LrcAPI":
			probes[i] = func() bool { return core.GlobalLrcAPI != nil && core.GlobalLrcAPI.CheckConnection() }
		case "AI":
			probes[i] = func() bool { return core.GlobalAI != nil && core.GlobalAI.CheckConnection() }
		case "QQMusicApi":
			endpoint := cfg.QQMusicApiConfig.Endpoint
			probes[i] = func() bool {
				return utils.CheckHealth(utils.HealthCheckOption{URL: endpoint, Method: http.MethodGet, Timeout: 2 * time.Second}).OK
			}
		case "n8n":
			endpoint := cfg.N8NConfig.N8NBaseUrl
			probes[i] = func() bool {
				return utils.CheckHealth(utils.HealthCheckOption{URL: endpoint, Method: http.MethodGet, Timeout: 2 * time.Second}).OK
			}
		}
	}

	results := make(chan probeResult, len(probes))
	pending := 0
	for i, probe := range probes {
		if probe == nil {
			continue
		}
		pending++
		go func(index int, check func() bool) {
			results <- probeResult{index: index, ok: check()}
		}(i, probe)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for pending > 0 {
		select {
		case result := <-results:
			services[result.index].Online = result.ok
			if result.ok {
				services[result.index].Detail = "在线"
			} else {
				services[result.index].Detail = "离线"
			}
			pending--
		case <-deadline.C:
			for i, probe := range probes {
				if probe != nil && services[i].Detail == "检测中" {
					services[i].Detail = "检查超时"
				}
			}
			pending = 0
		}
	}

	statusCacheMu.Lock()
	statusCache = services
	statusCheckedAt = time.Now()
	statusCacheMu.Unlock()
}
