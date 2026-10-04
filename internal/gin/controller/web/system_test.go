package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
	"github.com/nichuanfang/gymdl/config"
)

func TestRedactSecretsMasksWebUIAuthPassword(t *testing.T) {
	input := map[string]any{
		"web_config": map[string]any{
			"auth": map[string]any{"enable": true, "username": "operator", "password": "do-not-return"},
		},
	}
	redacted := redactSecrets(input).(map[string]any)
	auth := redacted["web_config"].(map[string]any)["auth"].(map[string]any)
	if auth["username"] != "operator" || auth["password"] != "" || auth["password_configured"] != true {
		t.Fatalf("unexpected redacted WebUI auth config: %#v", auth)
	}
}

func TestSystemConfigRedactsSecretsPersistsAndReportsReload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := []byte(`web_config:
  enable: true
  app_domain: localhost
  https: false
  app_port: 8080
  gin_mode: release
tidy:
  mode: 1
  dist_dir: data/dist
webdav:
  webdav_url: https://dav.example
  webdav_user: account
  webdav_pass: very-secret
  webdav_dir: Music
additional_config:
  enable_cron: false
  enable_monitor: false
  monitor_dirs: []
  music_mode: false
proxy:
  enable: true
  scheme: http
  host: proxy.local
  port: 8080
  user: proxy-user
  pass: proxy-secret
  auth: true
future_module:
  preserve: true
`)
	if err := os.WriteFile(configPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	SetWebConfig(&config.Config{
		ConfigFile:       configPath,
		WebConfig:        &config.WebConfig{Enable: true, AppDomain: "localhost", AppPort: 8080, GinMode: "release"},
		Tidy:             &config.TidyConfig{Mode: 1, DistDir: "data/dist"},
		WebDAV:           &config.WebDAVConfig{WebDAVUrl: "https://dav.example", WebDAVUser: "account", WebDAVPass: "very-secret", WebDAVDir: "Music"},
		Log:              &config.LogConfig{Mode: 1, Level: 2, File: "data/logs/run.log"},
		AdditionalConfig: &config.AdditionalConfig{MonitorDirs: []string{}},
	})
	SetConfigFilePath(configPath)
	manager = nil

	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/web/system/config", nil)
	HandleSystemConfig(getContext)
	var getResult struct {
		Data struct {
			Config map[string]any `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &getResult); err != nil {
		t.Fatal(err)
	}
	webdav := getResult.Data.Config["webdav"].(map[string]any)
	if webdav["webdav_pass"] != "" || webdav["webdav_pass_configured"] != true {
		t.Fatalf("secret should be hidden while configured marker is returned: %#v", webdav)
	}
	if getResult.Data.Config["log"].(map[string]any)["level"] != float64(2) {
		t.Fatal("runtime defaults should fill config sections omitted from the YAML file")
	}
	proxy := getResult.Data.Config["proxy"].(map[string]any)
	if proxy["pass"] != "" || proxy["pass_configured"] != true {
		t.Fatalf("proxy password should not be exposed: %#v", proxy)
	}
	if getResult.Data.Config["future_module"].(map[string]any)["preserve"] != true {
		t.Fatal("unknown config keys should be included")
	}

	tidy := getResult.Data.Config["tidy"].(map[string]any)
	tidy["dist_dir"] = "data/new-library"
	payload, err := json.Marshal(map[string]any{"config": getResult.Data.Config})
	if err != nil {
		t.Fatal(err)
	}
	putRecorder := httptest.NewRecorder()
	putContext, _ := gin.CreateTestContext(putRecorder)
	putContext.Request = httptest.NewRequest(http.MethodPut, "/api/web/system/config", bytes.NewReader(payload))
	putContext.Request.Header.Set("Content-Type", "application/json")
	HandleUpdateConfig(putContext)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("save failed with HTTP %d: %s", putRecorder.Code, putRecorder.Body.String())
	}
	var putResult struct {
		Code int `json:"code"`
		Data struct {
			RestartRequired bool     `json:"restart_required"`
			HotReloadFields []string `json:"hot_reload_fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(putRecorder.Body.Bytes(), &putResult); err != nil {
		t.Fatal(err)
	}
	if putResult.Code != http.StatusOK || putResult.Data.RestartRequired || len(putResult.Data.HotReloadFields) != 1 || putResult.Data.HotReloadFields[0] != "tidy.dist_dir" {
		t.Fatalf("unexpected reload report: %#v", putResult)
	}
	if _, err := os.Stat(configPath + ".bak"); err != nil {
		t.Fatalf("expected config backup: %v", err)
	}
	written, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := yaml.Unmarshal(written, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["webdav"].(map[string]any)["webdav_pass"] != "very-secret" {
		t.Fatal("blank masked secret should preserve the configured value")
	}
	if persisted["future_module"].(map[string]any)["preserve"] != true {
		t.Fatal("unrelated unknown config keys should survive save")
	}
	if persisted["proxy"].(map[string]any)["pass"] != "proxy-secret" {
		t.Fatal("blank proxy password should preserve its configured value")
	}
	if persisted["tidy"].(map[string]any)["dist_dir"] != "data/new-library" {
		t.Fatal("new library path was not persisted")
	}
}

func TestConfigValidationRejectsInvalidTidyMode(t *testing.T) {
	cfg := &config.Config{Tidy: &config.TidyConfig{Mode: 9, DistDir: "data/dist"}}
	if err := validateWebConfig(cfg); err == nil {
		t.Fatal("expected invalid tidy mode to be rejected")
	}
}
