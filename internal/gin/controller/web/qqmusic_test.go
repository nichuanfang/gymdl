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
  music_id: "654321"
  music_key: stale-config-key
  refresh_token: stale-config-refresh
`)
	if err := os.WriteFile(configPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	credentialPath := useQQMusicCredentialPath(t)
	if _, err := saveQQMusicCredential(credentialPath, map[string]interface{}{
		"musicid": "123456", "musickey": "old-key", "str_musicid": "123456", "openid": "old-open-id",
		"unionid": "old-union-id", "refresh_key": "old-refresh-key", "refresh_token": "old-refresh-token",
		"access_token": "old-access-token", "expired_at": "1900000000", "login_type": "0",
	}); err != nil {
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
			MusicId: "654321", MusicKey: "stale-config-key", RefreshToken: "stale-config-refresh",
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
	if string(persistedBytes) != string(initial) {
		t.Fatal("status refresh must not rewrite the bootstrap credentials in config.yaml")
	}
	storedBytes, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored qqMusicStoredCredential
	if err := json.Unmarshal(storedBytes, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.MusicID != 123456 || stored.MusicKey != "new-key" || stored.RefreshToken != "new-refresh-token" {
		t.Fatalf("refreshed runtime credential was not persisted to musickey.json: %#v", stored)
	}
	if info, err := os.Stat(credentialPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("musickey.json should be private (0600), info=%v err=%v", info, err)
	}
}

func TestQQLoginStatusBootstrapsMissingCacheFromConfigWithoutRewritingConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := []byte(`qq_music_api:
  enable: true
  endpoint: placeholder
  login_type: 0
  music_id: "123456"
  str_music_id: "123456"
  music_key: bootstrap-key
  refresh_key: bootstrap-refresh-key
  refresh_token: bootstrap-refresh-token
  access_token: bootstrap-access-token
`)
	if err := os.WriteFile(configPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	credentialPath := useQQMusicCredentialPath(t)

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
		cookie := r.Header.Get("Cookie")
		if !strings.Contains(cookie, "musicid=123456") || !strings.Contains(cookie, "musickey=bootstrap-key") {
			t.Errorf("bootstrap should use config.yaml credential: %q", cookie)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/login/check_expired":
			_, _ = w.Write([]byte(`{"code":-1,"msg":"操作失败"}`))
		case "/login/refresh_credential":
			_, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"musicid":123456,"musickey":"cache-key","str_musicid":"123456","openid":"open-id","unionid":"union-id","refresh_key":"refresh-key","refresh_token":"refresh-token","access_token":"access-token","expired_at":2000000000,"login_type":0}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	SetWebConfig(&config.Config{
		ConfigFile: configPath,
		QQMusicApiConfig: &config.QQMusicApiConfig{
			Enable: true, Endpoint: api.URL, LoginType: 0, MusicId: "123456", StrMusicId: "123456",
			MusicKey: "bootstrap-key", RefreshKey: "bootstrap-refresh-key", RefreshToken: "bootstrap-refresh-token", AccessToken: "bootstrap-access-token",
		},
	})
	SetConfigFilePath(configPath)
	manager = nil

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/qqmusic/status", nil)
	HandleQQLoginStatusOverview(ctx)
	if !strings.Contains(recorder.Body.String(), `"status":"logged_in"`) {
		t.Fatalf("expected successful bootstrap, got %s", recorder.Body.String())
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(configAfter) != string(initial) {
		t.Fatal("bootstrap refresh must not rewrite config.yaml")
	}
	cacheBytes, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatalf("expected musickey.json to be initialized: %v", err)
	}
	var cache qqMusicStoredCredential
	if err := json.Unmarshal(cacheBytes, &cache); err != nil {
		t.Fatal(err)
	}
	if cache.MusicKey != "cache-key" || cache.RefreshToken != "refresh-token" {
		t.Fatalf("unexpected initialized musickey cache: %#v", cache)
	}
}

func TestQQLoginStatusUsesMusickeyCacheBeforeStaleConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := []byte("qq_music_api:\n  enable: true\n  endpoint: placeholder\n  music_id: '654321'\n  music_key: stale-config-key\n")
	if err := os.WriteFile(configPath, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	credentialPath := useQQMusicCredentialPath(t)
	if _, err := saveQQMusicCredential(credentialPath, map[string]interface{}{
		"musicid": "123456", "musickey": "active-cache-key", "str_musicid": "123456",
		"login_type": "0", "expired_at": "9999999999",
	}); err != nil {
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
		if r.URL.Path != "/login/check_expired" {
			t.Errorf("unexpected QQMusicApi request path: %s", r.URL.Path)
		}
		cookie := r.Header.Get("Cookie")
		if !strings.Contains(cookie, "musicid=123456") || !strings.Contains(cookie, "musickey=active-cache-key") {
			t.Errorf("status check did not use musickey.json credential: %q", cookie)
		}
		if strings.Contains(cookie, "654321") || strings.Contains(cookie, "stale-config-key") {
			t.Errorf("status check used stale config.yaml credential: %q", cookie)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":-1,"msg":"操作失败"}`))
	}))
	defer api.Close()

	SetWebConfig(&config.Config{
		ConfigFile: configPath,
		QQMusicApiConfig: &config.QQMusicApiConfig{
			Enable: true, Endpoint: api.URL, LoginType: 0, MusicId: "654321", MusicKey: "stale-config-key",
		},
	})
	SetConfigFilePath(configPath)
	manager = nil

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/web/qqmusic/status", nil)
	HandleQQLoginStatusOverview(ctx)
	if !strings.Contains(recorder.Body.String(), `"status":"logged_in"`) || !strings.Contains(recorder.Body.String(), `"account":"****3456"`) {
		t.Fatalf("expected logged_in status for the musickey.json account, got %s", recorder.Body.String())
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(configAfter) != string(initial) {
		t.Fatal("reading QQ login status must not rewrite config.yaml")
	}
}

func TestQQMusicCredentialSavePreservesExactStringMusicID(t *testing.T) {
	stored, err := qqMusicStoredCredentialFromMap(map[string]interface{}{
		// JSON map decoding represents musicid as float64 and rounds this value.
		"musicid":       float64(1152921504885411072),
		"str_musicid":   "1152921504885410999",
		"musickey":      "test-key",
		"login_type":    float64(0),
		"expired_at":    float64(1900000000),
		"refresh_token": "test-refresh-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.MusicID != 1152921504885410999 {
		t.Fatalf("expected exact str_musicid to prevent float64 precision loss, got %d", stored.MusicID)
	}
}

func TestQQLoginStatusKeepsExpiredWhenAutomaticRefreshFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("qq_music_api: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credentialPath := useQQMusicCredentialPath(t)
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
	if _, err := os.Stat(credentialPath); !os.IsNotExist(err) {
		t.Fatalf("failed refresh should not create a credential cache, err=%v", err)
	}
}

func useQQMusicCredentialPath(t *testing.T) string {
	t.Helper()
	previous := qqMusicCredentialPath
	path := filepath.Join(t.TempDir(), "data", "temp", "musickey.json")
	qqMusicCredentialPath = path
	t.Cleanup(func() { qqMusicCredentialPath = previous })
	return path
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
	credentialPath := useQQMusicCredentialPath(t)

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
	if string(persistedBytes) != string(initial) {
		t.Fatal("QR login must not write runtime credentials into config.yaml")
	}
	storedBytes, err := os.ReadFile(credentialPath)
	if err != nil {
		t.Fatal(err)
	}
	var stored qqMusicStoredCredential
	if err := json.Unmarshal(storedBytes, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.MusicID != 123456 || stored.MusicKey != "private-music-key" || stored.RefreshToken != "private-refresh-token" {
		t.Fatalf("credential was not persisted to musickey.json: %#v", stored)
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
	if redacted["music_key"] != "" || redacted["music_key_configured"] != false {
		t.Fatalf("runtime musickey cache must not appear as a config.yaml secret: %#v", redacted)
	}
}
