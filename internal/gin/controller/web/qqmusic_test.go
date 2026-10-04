package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
	"github.com/nichuanfang/gymdl/config"
)

func TestClassifyQQExpiredResponseSupportsQQMusicApiBoolEnvelope(t *testing.T) {
	okCode := -1
	expiredCode := 0
	cases := []struct {
		name    string
		result  qqMusicAPIResponse
		expired bool
		known   bool
	}{
		{name: "legacy bool payload", result: qqMusicAPIResponse{Data: true}, expired: true, known: true},
		{name: "expired bool wrapped as success", result: qqMusicAPIResponse{Code: &expiredCode}, expired: true, known: true},
		{name: "valid bool wrapped as operation false", result: qqMusicAPIResponse{Code: &okCode, Msg: "操作失败"}, expired: false, known: true},
		{name: "unknown response", result: qqMusicAPIResponse{Code: &okCode, Msg: "unexpected"}, known: false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			expired, known := classifyQQExpired(test.result)
			if expired != test.expired || known != test.known {
				t.Fatalf("classifyQQExpired() = (%v, %v), want (%v, %v)", expired, known, test.expired, test.known)
			}
		})
	}
}

func TestQQLoginStatusRefreshesExpiredMusicKeyAndPersistsNewCredential(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := []byte(`qq_music_api:
  enable: true
  endpoint: placeholder
  login_type: 0
  music_id: "123456"
  music_key: old-key
  open_id: old-open-id
  union_id: old-union-id
  refresh_key: old-refresh-key
  refresh_token: old-refresh-token
  access_token: old-access-token
  expired_at: 1900000000
`)
	if err := os.WriteFile(configPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	previousConfig := GetWebConfig()
	previousPath := configFilePath
	previousManager := manager
	savedConfigMu.RLock()
	previousSaved := cloneYAMLMap(savedConfig)
	savedConfigMu.RUnlock()
	defer func() {
		SetWebConfig(previousConfig)
		configFilePath = previousPath
		manager = previousManager
		savedConfigMu.Lock()
		savedConfig = previousSaved
		savedConfigMu.Unlock()
	}()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); !strings.Contains(got, "musicid=123456") || !strings.Contains(got, "musickey=old-key") || !strings.Contains(got, "login_type=0") {
			t.Errorf("QQMusicApi request did not use the saved credential cookie: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/check_expired":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
		case "/login/refresh_credential":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"musicid":123456,"musickey":"new-key","str_musicid":"123456","openid":"new-open-id","unionid":"new-union-id","refresh_key":"new-refresh-key","refresh_token":"new-refresh-token","access_token":"new-access-token","expired_at":2000000000,"login_type":0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	SetWebConfig(&config.Config{
		ConfigFile: configPath,
		QQMusicApiConfig: &config.QQMusicApiConfig{
			Enable: true, Endpoint: api.URL, LoginType: 0,
			MusicId: "123456", MusicKey: "old-key", OpenID: "old-open-id", UnionID: "old-union-id",
			RefreshKey: "old-refresh-key", RefreshToken: "old-refresh-token", AccessToken: "old-access-token",
			ExpiredAt: 1900000000,
		},
	})
	SetConfigFilePath(configPath)
	manager = nil

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/qqmusic/status", nil)
	HandleQQLoginStatusOverview(ctx)

	var result struct {
		Data struct {
			Status    string `json:"status"`
			Refreshed bool   `json:"refreshed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Status != "logged_in" || !result.Data.Refreshed {
		t.Fatalf("expected a successful silent refresh, got %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "new-key") || strings.Contains(recorder.Body.String(), "new-refresh-token") {
		t.Fatal("status response exposed refreshed QQ credentials")
	}
	if active := GetWebConfig().QQMusicApiConfig; active.MusicKey != "new-key" || active.RefreshToken != "new-refresh-token" {
		t.Fatalf("refreshed credential was not applied to active config: %#v", active)
	}
	persistedBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := yaml.Unmarshal(persistedBytes, &persisted); err != nil {
		t.Fatal(err)
	}
	if got := persisted["qq_music_api"].(map[string]any)["music_key"]; got != "new-key" {
		t.Fatalf("refreshed credential was not persisted: music_key=%v", got)
	}
}

func TestQQLoginStatusKeepsExpiredWhenAutomaticRefreshFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("qq_music_api: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previousConfig := GetWebConfig()
	previousPath := configFilePath
	previousManager := manager
	savedConfigMu.RLock()
	previousSaved := cloneYAMLMap(savedConfig)
	savedConfigMu.RUnlock()
	defer func() {
		SetWebConfig(previousConfig)
		configFilePath = previousPath
		manager = previousManager
		savedConfigMu.Lock()
		savedConfig = previousSaved
		savedConfigMu.Unlock()
	}()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/login/check_expired" {
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":null}`))
			return
		}
		http.Error(w, "refresh failed", http.StatusUnauthorized)
	}))
	defer api.Close()
	SetWebConfig(&config.Config{
		ConfigFile: configPath,
		QQMusicApiConfig: &config.QQMusicApiConfig{
			Enable: true, Endpoint: api.URL, MusicId: "123456", MusicKey: "old-key", RefreshToken: "expired-refresh-token",
		},
	})
	SetConfigFilePath(configPath)
	manager = nil

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/qqmusic/status", nil)
	HandleQQLoginStatusOverview(ctx)
	if !strings.Contains(recorder.Body.String(), `"status":"logged_out"`) {
		t.Fatalf("expected logged_out after failed refresh, got %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "expired-refresh-token") {
		t.Fatal("status response exposed QQ refresh credentials")
	}
}

func TestQRLoginCredentialIsPersistedPrivatelyAndAppliedToFutureTasks(t *testing.T) {
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
qq_music_api:
  enable: true
  endpoint: http://qqmusic-api.local
  login_type: 0
`)
	if err := os.WriteFile(configPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}

	previousConfig := GetWebConfig()
	previousPath := configFilePath
	previousManager := manager
	savedConfigMu.RLock()
	previousSaved := cloneYAMLMap(savedConfig)
	savedConfigMu.RUnlock()
	defer func() {
		SetWebConfig(previousConfig)
		configFilePath = previousPath
		manager = previousManager
		savedConfigMu.Lock()
		savedConfig = previousSaved
		savedConfigMu.Unlock()
	}()

	SetWebConfig(&config.Config{
		ConfigFile: configPath,
		WebConfig:  &config.WebConfig{Enable: true, AppDomain: "localhost", AppPort: 8080, GinMode: "release"},
		Tidy:       &config.TidyConfig{Mode: 1, DistDir: "data/dist"},
		QQMusicApiConfig: &config.QQMusicApiConfig{
			Enable: true, Endpoint: "http://qqmusic-api.local", LoginType: 0,
		},
	})
	SetConfigFilePath(configPath)
	manager = nil

	credential := map[string]interface{}{
		"musicid": float64(123456), "musickey": "private-music-key", "str_musicid": "123456",
		"openid": "private-open-id", "unionid": "private-union-id", "refresh_key": "private-refresh-key",
		"refresh_token": "private-refresh-token", "access_token": "private-access-token",
		"expired_at": float64(1900000000), "loginType": float64(0),
	}
	if err := persistQQLoginCredential(credential); err != nil {
		t.Fatalf("persistQQLoginCredential() error: %v", err)
	}

	active := GetWebConfig()
	if active.QQMusicApiConfig.MusicId != "123456" || active.QQMusicApiConfig.MusicKey != "private-music-key" {
		t.Fatalf("credentials were not applied to active config: %#v", active.QQMusicApiConfig)
	}
	taskConfig := config.WithLiveTaskOverrides(&config.Config{
		QQMusicApiConfig: &config.QQMusicApiConfig{Enable: true, Endpoint: "http://qqmusic-api.local"},
	})
	if taskConfig.QQMusicApiConfig.MusicKey != "private-music-key" {
		t.Fatal("new task snapshot did not receive the saved QQ credential")
	}

	persistedBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := yaml.Unmarshal(persistedBytes, &persisted); err != nil {
		t.Fatal(err)
	}
	qqConfig := persisted["qq_music_api"].(map[string]any)
	if qqConfig["music_key"] != "private-music-key" || qqConfig["refresh_token"] != "private-refresh-token" {
		t.Fatalf("credential was not persisted: %#v", qqConfig)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/system/config", nil)
	HandleSystemConfig(ctx)
	if strings.Contains(recorder.Body.String(), "private-music-key") || strings.Contains(recorder.Body.String(), "private-refresh-token") {
		t.Fatal("raw QQ credentials were exposed by the settings API")
	}
	var response struct {
		Data struct {
			Config map[string]any `json:"config"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	redacted := response.Data.Config["qq_music_api"].(map[string]any)
	if redacted["music_key"] != "" || redacted["music_key_configured"] != true {
		t.Fatalf("expected masked QQ credential marker: %#v", redacted)
	}
}
