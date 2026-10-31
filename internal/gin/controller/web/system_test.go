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

func TestSystemConfigMovesTidyPlaylistToSidecarOnFirstSave(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	initial := []byte(`web_config:
  enable: true
  app_domain: localhost
  https: false
  app_port: 8080
  gin_mode: release
tidy:
  mode: 1
  dist_dir: data/dist
n8n_config:
  enable: true
  tidy_playlist_endpoint: https://n8n.example/webhook/tidy
  playlist_assist: false
  tidy_playlist:
    - name: 中文
      desc: inline copy
`)
	if err := os.WriteFile(configPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ConfigFile:       configPath,
		WebConfig:        &config.WebConfig{Enable: true, AppDomain: "localhost", AppPort: 8080, GinMode: "release"},
		Tidy:             &config.TidyConfig{Mode: 1, DistDir: "data/dist"},
		WebDAV:           &config.WebDAVConfig{},
		Log:              &config.LogConfig{Mode: 1, Level: 2, File: "data/logs/run.log"},
		Telegram:         &config.TelegramConfig{Mode: 1},
		LrcAPI:           &config.LrcAPIConfig{},
		AI:               &config.AIConfig{},
		N8NConfig:        &config.N8NConfig{Enable: true, TidyPlaylistEndpoint: "https://n8n.example/webhook/tidy", TidyPlaylist: []config.PlaylistInfo{{Name: "中文", Desc: "inline copy"}}},
		QQMusicApiConfig: &config.QQMusicApiConfig{},
		AdditionalConfig: &config.AdditionalConfig{MonitorDirs: []string{}},
		ProxyConfig:      &config.ProxyConfig{},
	}
	SetWebConfig(cfg)
	SetConfigFilePath(configPath)
	manager = nil
	restore := snapshotSystemConfigState()
	defer restore()

	payload := saveCurrentConfigPayload(t, func(cfg map[string]any) {
		n8n := cfg["n8n_config"].(map[string]any)
		n8n["tidy_playlist"] = []any{
			map[string]any{"name": "sidecar one", "desc": "first"},
			map[string]any{"name": "sidecar two", "desc": "second"},
		}
	})
	putRecorder := httptest.NewRecorder()
	putContext, _ := gin.CreateTestContext(putRecorder)
	putContext.Request = httptest.NewRequest(http.MethodPut, "/api/web/system/config", bytes.NewReader(payload))
	putContext.Request.Header.Set("Content-Type", "application/json")
	HandleUpdateConfig(putContext)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("first save failed HTTP %d: %s", putRecorder.Code, putRecorder.Body.String())
	}

	playlistPath := config.PlaylistFilePath(configPath)
	entries, present, err := config.LoadPlaylistFile(playlistPath)
	if err != nil || !present || len(entries) != 2 || entries[0].Name != "sidecar one" {
		t.Fatalf("sidecar was not written: entries=%#v present=%v err=%v", entries, present, err)
	}
	info, err := os.Stat(playlistPath)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("sidecar permission is too open: %v %v", info, err)
	}
	mainDoc := readMainConfigMap(t, configPath)
	if _, exists := mainDoc["n8n_config"].(map[string]any)["tidy_playlist"]; exists {
		t.Fatal("inline tidy_playlist must be removed after the first save")
	}
	if _, err := os.Stat(configPath + ".bak"); err != nil {
		t.Fatalf("expected main config backup: %v", err)
	}

	SetConfigFilePath(configPath)
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
	loaded := getResult.Data.Config["n8n_config"].(map[string]any)["tidy_playlist"].([]any)
	if len(loaded) != 2 || loaded[1].(map[string]any)["name"] != "sidecar two" {
		t.Fatalf("reloaded config should show sidecar playlists: %#v", loaded)
	}

	payload = saveCurrentConfigPayload(t, func(cfg map[string]any) {
		n8n := cfg["n8n_config"].(map[string]any)
		n8n["tidy_playlist"] = []any{map[string]any{"name": "only", "desc": "second save"}}
	})
	putRecorder = httptest.NewRecorder()
	putContext, _ = gin.CreateTestContext(putRecorder)
	putContext.Request = httptest.NewRequest(http.MethodPut, "/api/web/system/config", bytes.NewReader(payload))
	putContext.Request.Header.Set("Content-Type", "application/json")
	HandleUpdateConfig(putContext)
	if putRecorder.Code != http.StatusOK {
		t.Fatalf("second save failed HTTP %d: %s", putRecorder.Code, putRecorder.Body.String())
	}
	entries, _, err = config.LoadPlaylistFile(playlistPath)
	if err != nil || len(entries) != 1 || entries[0].Name != "only" {
		t.Fatalf("second save did not replace sidecar: %#v err=%v", entries, err)
	}
	if mainDoc = readMainConfigMap(t, configPath); mainDoc["n8n_config"].(map[string]any)["tidy_playlist"] != nil {
		t.Fatal("repeated saves must not reintroduce inline playlists")
	}
}

func TestSystemConfigRollsBackSidecarWhenMainConfigWriteFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.Mkdir(configPath, 0o700); err != nil {
		t.Fatal(err)
	}
	playlistPath := config.PlaylistFilePath(configPath)
	previous := []byte("tidy_playlist:\n  - name: previous\n    desc: keep\n")
	if err := os.WriteFile(playlistPath, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ConfigFile:       configPath,
		WebConfig:        &config.WebConfig{Enable: true, AppDomain: "localhost", AppPort: 8080, GinMode: "release"},
		Tidy:             &config.TidyConfig{Mode: 1, DistDir: "data/dist"},
		WebDAV:           &config.WebDAVConfig{},
		Log:              &config.LogConfig{Mode: 1, Level: 2, File: "data/logs/run.log"},
		Telegram:         &config.TelegramConfig{Mode: 1},
		LrcAPI:           &config.LrcAPIConfig{},
		AI:               &config.AIConfig{},
		N8NConfig:        &config.N8NConfig{TidyPlaylist: []config.PlaylistInfo{{Name: "previous", Desc: "keep"}}},
		QQMusicApiConfig: &config.QQMusicApiConfig{},
		AdditionalConfig: &config.AdditionalConfig{MonitorDirs: []string{}},
		ProxyConfig:      &config.ProxyConfig{},
	}
	SetWebConfig(cfg)
	SetConfigFilePath(configPath)
	manager = nil
	restore := snapshotSystemConfigState()
	defer restore()

	payload := saveCurrentConfigPayload(t, func(doc map[string]any) {
		doc["n8n_config"] = map[string]any{"tidy_playlist": []any{map[string]any{"name": "replacement", "desc": "new"}}}
	})
	putRecorder := httptest.NewRecorder()
	putContext, _ := gin.CreateTestContext(putRecorder)
	putContext.Request = httptest.NewRequest(http.MethodPut, "/api/web/system/config", bytes.NewReader(payload))
	putContext.Request.Header.Set("Content-Type", "application/json")
	HandleUpdateConfig(putContext)
	if putRecorder.Code == http.StatusOK {
		t.Fatalf("expected save failure when main config path is a directory: %s", putRecorder.Body.String())
	}
	after, err := os.ReadFile(playlistPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(previous) {
		t.Fatalf("sidecar was not rolled back:\n%s", after)
	}
	entries, present, err := config.LoadPlaylistFile(playlistPath)
	if err != nil || !present || len(entries) != 1 || entries[0].Name != "previous" {
		t.Fatalf("rollback produced invalid sidecar: %#v present=%v err=%v", entries, present, err)
	}
}

func snapshotSystemConfigState() func() {
	previousPath := configFilePath
	savedConfigMu.RLock()
	previousSaved := cloneYAMLMap(savedConfig)
	savedConfigMu.RUnlock()
	previousWeb := GetWebConfig()
	return func() {
		configFilePath = previousPath
		savedConfigMu.Lock()
		savedConfig = previousSaved
		savedConfigMu.Unlock()
		if previousWeb != nil {
			SetWebConfig(previousWeb)
		}
	}
}

func saveCurrentConfigPayload(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	getRecorder := httptest.NewRecorder()
	getContext, _ := gin.CreateTestContext(getRecorder)
	getContext.Request = httptest.NewRequest(http.MethodGet, "/api/web/system/config", nil)
	HandleSystemConfig(getContext)
	var result struct {
		Data struct {
			Config map[string]any `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(getRecorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	mutate(result.Data.Config)
	payload, err := json.Marshal(map[string]any{"config": result.Data.Config})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func readMainConfigMap(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestSystemConfigRemovesNewSidecarOnFailedFirstSave(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.Mkdir(configPath, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		ConfigFile:       configPath,
		WebConfig:        &config.WebConfig{Enable: true, AppDomain: "localhost", AppPort: 8080, GinMode: "release"},
		Tidy:             &config.TidyConfig{Mode: 1, DistDir: "data/dist"},
		WebDAV:           &config.WebDAVConfig{},
		Log:              &config.LogConfig{Mode: 1, Level: 2, File: "data/logs/run.log"},
		Telegram:         &config.TelegramConfig{Mode: 1},
		LrcAPI:           &config.LrcAPIConfig{},
		AI:               &config.AIConfig{},
		N8NConfig:        &config.N8NConfig{},
		QQMusicApiConfig: &config.QQMusicApiConfig{},
		AdditionalConfig: &config.AdditionalConfig{MonitorDirs: []string{}},
		ProxyConfig:      &config.ProxyConfig{},
	}
	SetWebConfig(cfg)
	SetConfigFilePath(configPath)
	manager = nil
	restore := snapshotSystemConfigState()
	defer restore()

	payload := saveCurrentConfigPayload(t, func(doc map[string]any) {
		doc["n8n_config"] = map[string]any{"tidy_playlist": []any{map[string]any{"name": "new"}}}
	})
	putRecorder := httptest.NewRecorder()
	putContext, _ := gin.CreateTestContext(putRecorder)
	putContext.Request = httptest.NewRequest(http.MethodPut, "/api/web/system/config", bytes.NewReader(payload))
	putContext.Request.Header.Set("Content-Type", "application/json")
	HandleUpdateConfig(putContext)
	if putRecorder.Code == http.StatusOK {
		t.Fatal("expected save to fail")
	}
	for _, candidate := range []string{config.PlaylistFilePath(configPath), config.PlaylistFilePath(configPath) + ".bak"} {
		if _, err := os.Stat(candidate); !os.IsNotExist(err) {
			t.Fatalf("failed first save must not leave %s: %v", candidate, err)
		}
	}
}
