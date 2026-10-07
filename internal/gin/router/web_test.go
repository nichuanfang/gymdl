package router

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/gin/controller/web"
	"github.com/nichuanfang/gymdl/internal/gin/task"
)

func TestSearchStreamRouteUsesExistingWebAuthMiddleware(t *testing.T) {
	t.Setenv("GYMDL_WEBUI_AUTH_ENABLED", "true")
	t.Setenv("GYMDL_WEBUI_USERNAME", "admin")
	t.Setenv("GYMDL_WEBUI_PASSWORD", "test-password")
	previousConfig := web.GetWebConfig()
	t.Cleanup(func() {
		web.SetWebConfig(previousConfig)
		web.SetTaskManager(nil)
	})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	cfg := &config.Config{
		WebConfig:        &config.WebConfig{Enable: true, AppDomain: "localhost", AppPort: 8080, GinMode: "test"},
		AdditionalConfig: &config.AdditionalConfig{},
	}
	tasks := task.NewTaskManagerWithDBPath(cfg, filepath.Join(t.TempDir(), "gymdl.sqlite3"), "")
	if err := tasks.InitializationError(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tasks.Close() })
	SetupWebRoutesWithTaskManager(engine.Group("/api"), cfg, tasks)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/web/search/stream?keyword=auth-test&platform=netease", nil)
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("search stream must be covered by WebUI auth, got status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Content-Type") == "text/event-stream; charset=utf-8" {
		t.Fatal("unauthenticated request unexpectedly started an SSE response")
	}
}
