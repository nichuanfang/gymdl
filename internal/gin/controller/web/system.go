package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/gin/middleware"
	"github.com/nichuanfang/gymdl/internal/gin/response"
)

var (
	webCfgMu       sync.RWMutex
	webCfg         *config.Config
	configFilePath string
	savedConfigMu  sync.RWMutex
	savedConfig    map[string]any
)

var secretKeys = map[string]struct{}{
	"webdav_pass": {}, "cookiecloud_uuid": {}, "cookiecloud_key": {},
	"bot_token": {}, "api_key": {}, "auth_token": {}, "webhook_url": {},
	"proxy_pass": {}, "pass": {}, "password": {}, "token": {}, "secret": {}, "user": {}, "refresh_key": {}, "refresh_token": {},
	"access_token": {}, "music_id": {}, "str_music_id": {}, "open_id": {},
	"union_id": {}, "music_key": {},
}

// SetWebConfig installs a new immutable runtime snapshot.
func SetWebConfig(c *config.Config) {
	webCfgMu.Lock()
	webCfg = cloneWebConfig(c)
	active := webCfg
	webCfgMu.Unlock()
	config.SetLiveRuntimeConfig(active)
}

// GetWebConfig returns the current immutable runtime config snapshot.
func GetWebConfig() *config.Config {
	webCfgMu.RLock()
	defer webCfgMu.RUnlock()
	return webCfg
}

func replaceWebConfig(c *config.Config) {
	webCfgMu.Lock()
	webCfg = cloneWebConfig(c)
	webCfgMu.Unlock()
}

func cloneWebConfig(c *config.Config) *config.Config {
	if c == nil {
		return &config.Config{}
	}
	b, err := yaml.Marshal(c)
	if err == nil {
		var out config.Config
		if yaml.Unmarshal(b, &out) == nil {
			out.ConfigFile = c.ConfigFile
			return &out
		}
	}
	out := *c
	return &out
}

// SetConfigFilePath injects the YAML path and loads a raw document so edits preserve
// keys that are not yet represented by this binary's Config struct.
func SetConfigFilePath(path string) {
	configFilePath = path
	if path == "" {
		return
	}
	if data, err := os.ReadFile(path); err == nil {
		var doc map[string]any
		if yaml.Unmarshal(data, &doc) == nil && doc != nil {
			doc = withConfigDefaults(doc)
			savedConfigMu.Lock()
			savedConfig = doc
			savedConfigMu.Unlock()
			return
		}
	}
	if cfg := GetWebConfig(); cfg != nil {
		if data, err := yaml.Marshal(cfg); err == nil {
			var doc map[string]any
			if yaml.Unmarshal(data, &doc) == nil {
				savedConfigMu.Lock()
				savedConfig = doc
				savedConfigMu.Unlock()
			}
		}
	}
}

func withConfigDefaults(doc map[string]any) map[string]any {
	cfg := GetWebConfig()
	if cfg == nil {
		return doc
	}
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		return doc
	}
	var defaults map[string]any
	if err := yaml.Unmarshal(encoded, &defaults); err != nil || defaults == nil {
		return doc
	}
	return mergeConfigMaps(defaults, doc, nil)
}

// HandleSystemConfig GET /api/web/system/config
func HandleSystemConfig(c *gin.Context) {
	savedConfigMu.RLock()
	doc := cloneYAMLMap(savedConfig)
	savedConfigMu.RUnlock()
	if doc == nil {
		response.Fail(c, http.StatusInternalServerError, "无法读取配置")
		return
	}
	response.Success(c, gin.H{"config": redactSecrets(doc)})
}

// HandleUpdateConfig PUT /api/web/system/config
func HandleUpdateConfig(c *gin.Context) {
	var request struct {
		Config      map[string]any `json:"config" binding:"required"`
		ClearSecret []string       `json:"clear_secrets"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || request.Config == nil {
		response.Fail(c, http.StatusBadRequest, "参数错误：需要提供完整 config 对象")
		return
	}
	if configFilePath == "" {
		response.Fail(c, http.StatusInternalServerError, "配置文件路径未初始化")
		return
	}

	savedConfigMu.Lock()
	defer savedConfigMu.Unlock()
	before := cloneYAMLMap(savedConfig)
	if before == nil {
		response.Fail(c, http.StatusInternalServerError, "无法读取当前配置")
		return
	}
	incoming := stripConfigMetadata(request.Config)
	merged := mergeConfigMaps(before, incoming, nil)
	for _, path := range request.ClearSecret {
		if !isSecretPath(path) {
			response.Fail(c, http.StatusBadRequest, "不允许清除非密钥字段: "+path)
			return
		}
		setConfigPath(merged, strings.Split(path, "."), "")
	}

	encoded, err := yaml.Marshal(merged)
	if err != nil {
		response.Fail(c, http.StatusBadRequest, "配置无法序列化: "+err.Error())
		return
	}
	var next config.Config
	if err := yaml.Unmarshal(encoded, &next); err != nil {
		response.Fail(c, http.StatusBadRequest, "配置格式错误: "+err.Error())
		return
	}
	next.ConfigFile = configFilePath
	if err := validateWebConfig(&next); err != nil {
		response.Fail(c, http.StatusBadRequest, err.Error())
		return
	}
	if err := writeConfigAtomically(configFilePath, encoded); err != nil {
		response.Fail(c, http.StatusInternalServerError, "保存配置失败: "+err.Error())
		return
	}

	changed := changedConfigPaths(before, merged, "")
	hotReload := make([]string, 0, 2)
	restartRequired := make([]string, 0)
	for _, path := range changed {
		if path == "tidy.dist_dir" || path == "additional_config.music_mode" {
			hotReload = append(hotReload, path)
		} else {
			restartRequired = append(restartRequired, path)
		}
	}
	sort.Strings(hotReload)
	sort.Strings(restartRequired)

	active := cloneWebConfig(GetWebConfig())
	if active.Tidy != nil && next.Tidy != nil {
		active.Tidy.DistDir = next.Tidy.DistDir
	}
	if active.AdditionalConfig != nil && next.AdditionalConfig != nil {
		active.AdditionalConfig.MusicMode = next.AdditionalConfig.MusicMode
	}
	replaceWebConfig(active)
	config.SetLiveRuntimeConfig(active)
	if manager != nil {
		manager.SetConfig(active)
	}
	savedConfig = merged
	response.Success(c, gin.H{
		"updated":           true,
		"hot_reload_fields": hotReload,
		"restart_required":  len(restartRequired) > 0,
		"restart_fields":    restartRequired,
		"config":            redactSecrets(cloneYAMLMap(merged)),
	})
}

// persistQQLoginCredential stores QR credentials in the runtime cache without
// writing them into config.yaml or returning them to the browser.
func persistQQLoginCredential(credential map[string]interface{}) error {
	stored, err := saveQQMusicCredential(qqMusicCredentialPath, credential)
	if err != nil {
		return err
	}
	var base *config.QQMusicApiConfig
	if active := GetWebConfig(); active != nil {
		base = active.QQMusicApiConfig
	}
	applyQQMusicCredentialToRuntime(stored.toConfig(base))
	return nil
}

func credentialString(credential map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := credential[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed)
		case float64:
			return strconv.FormatInt(int64(typed), 10)
		case int, int32, int64, uint, uint32, uint64:
			return fmt.Sprint(typed)
		}
	}
	return ""
}

func credentialInt(credential map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := credential[key]
		if !ok || value == nil {
			continue
		}
		if numeric, ok := numericConfigValue(value); ok {
			return int(numeric), true
		}
		if text, ok := value.(string); ok {
			parsed, err := strconv.Atoi(strings.TrimSpace(text))
			if err == nil {
				return parsed, true
			}
		}
	}
	return 0, false
}

func validateWebConfig(c *config.Config) error {
	if c.WebConfig != nil {
		if c.WebConfig.AppPort < 1 || c.WebConfig.AppPort > 65535 {
			return errors.New("web_config.app_port 必须在 1 到 65535 之间")
		}
		if c.WebConfig.GinMode != "debug" && c.WebConfig.GinMode != "release" && c.WebConfig.GinMode != "test" {
			return errors.New("web_config.gin_mode 必须是 debug、release 或 test")
		}
		if err := middleware.ValidateWebAuthSettings(c.WebConfig.Auth); err != nil {
			return err
		}
	}
	if c.Tidy != nil {
		if c.Tidy.Mode != 1 && c.Tidy.Mode != 2 {
			return errors.New("tidy.mode 只能是 1 或 2")
		}
		if strings.TrimSpace(c.Tidy.DistDir) == "" {
			return errors.New("tidy.dist_dir 不能为空")
		}
		if c.Tidy.Mode == 2 && (c.WebDAV == nil || strings.TrimSpace(c.WebDAV.WebDAVUrl) == "" || strings.TrimSpace(c.WebDAV.WebDAVUser) == "" || strings.TrimSpace(c.WebDAV.WebDAVPass) == "") {
			return errors.New("tidy.mode=2 时必须配置 WebDAV 地址、用户名和密码")
		}
	}
	if c.CookieCloud != nil && c.CookieCloud.Mode != 1 && c.CookieCloud.Mode != 2 {
		return errors.New("cookie_cloud.mode 只能是 1 或 2")
	}
	if c.ProxyConfig != nil && c.ProxyConfig.Enable {
		if c.ProxyConfig.Scheme != "http" && c.ProxyConfig.Scheme != "socks5" {
			return errors.New("proxy.scheme 必须是 http 或 socks5")
		}
		if strings.TrimSpace(c.ProxyConfig.Host) == "" || c.ProxyConfig.Port < 1 || c.ProxyConfig.Port > 65535 {
			return errors.New("启用代理时必须填写有效的 proxy.host 和 proxy.port")
		}
	}
	if c.Telegram != nil && c.Telegram.Enable && c.Telegram.Mode != 1 && c.Telegram.Mode != 2 {
		return errors.New("telegram.mode 只能是 1 或 2")
	}
	if c.QQMusicApiConfig != nil && c.QQMusicApiConfig.Enable && strings.TrimSpace(c.QQMusicApiConfig.Endpoint) == "" {
		return errors.New("启用 QQ Music API 时必须填写 endpoint")
	}
	if c.LrcAPI != nil && c.LrcAPI.Enable && strings.TrimSpace(c.LrcAPI.LrcApiUrl) == "" {
		return errors.New("启用 LrcAPI 时必须填写 lrc_api_url")
	}
	if c.Log != nil {
		if c.Log.Mode < 1 || c.Log.Mode > 3 {
			return errors.New("log.mode 必须在 1 到 3 之间")
		}
		if c.Log.Level < 1 || c.Log.Level > 5 {
			return errors.New("log.level 必须在 1 到 5 之间")
		}
	}
	if c.QQMusicApiConfig != nil && (c.QQMusicApiConfig.LoginType < 0 || c.QQMusicApiConfig.LoginType > 2) {
		return errors.New("qq_music_api.login_type 必须在 0 到 2 之间")
	}
	return nil
}

func writeConfigAtomically(path string, data []byte) error {
	info, statErr := os.Stat(path)
	mode := os.FileMode(0o600)
	if statErr == nil {
		mode = info.Mode().Perm()
		if mode&0o077 != 0 {
			mode &= 0o600
		}
	}
	backup := path + ".bak"
	if old, err := os.ReadFile(path); err == nil {
		if err := os.WriteFile(backup, old, mode); err != nil {
			return fmt.Errorf("创建备份失败: %w", err)
		}
		if err := os.Chmod(backup, mode); err != nil {
			return fmt.Errorf("保护配置备份失败: %w", err)
		}
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".gymdl-config-*.tmp")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}

func cloneYAMLMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	encoded, err := yaml.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if yaml.Unmarshal(encoded, &out) != nil {
		return nil
	}
	return out
}

func redactSecrets(value any) any {
	switch item := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(item)+4)
		for key, child := range item {
			if _, secret := secretKeys[strings.ToLower(key)]; secret {
				out[key] = ""
				out[key+"_configured"] = child != nil && fmt.Sprint(child) != ""
				continue
			}
			out[key] = redactSecrets(child)
		}
		return out
	case []any:
		out := make([]any, len(item))
		for i, child := range item {
			out[i] = redactSecrets(child)
		}
		return out
	default:
		return value
	}
}

func stripConfigMetadata(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, child := range value {
		if strings.HasSuffix(key, "_configured") {
			baseKey := strings.TrimSuffix(key, "_configured")
			if _, isSecret := secretKeys[strings.ToLower(baseKey)]; isSecret {
				continue
			}
		}
		if nested, ok := child.(map[string]any); ok {
			out[key] = stripConfigMetadata(nested)
		} else {
			out[key] = child
		}
	}
	return out
}

func mergeConfigMaps(base, incoming map[string]any, prefix []string) map[string]any {
	out := cloneYAMLMap(base)
	if out == nil {
		out = make(map[string]any)
	}
	for key, value := range incoming {
		path := append(append([]string(nil), prefix...), key)
		if nested, ok := value.(map[string]any); ok {
			old, _ := out[key].(map[string]any)
			out[key] = mergeConfigMaps(old, nested, path)
			continue
		}
		if _, secret := secretKeys[strings.ToLower(key)]; secret && isEmptyValue(value) {
			continue
		}
		out[key] = value
	}
	return out
}

func isEmptyValue(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func isSecretPath(path string) bool {
	parts := strings.Split(path, ".")
	if len(parts) == 0 {
		return false
	}
	_, ok := secretKeys[strings.ToLower(parts[len(parts)-1])]
	return ok
}

func setConfigPath(root map[string]any, path []string, value any) {
	if len(path) == 0 {
		return
	}
	current := root
	for _, key := range path[:len(path)-1] {
		next, _ := current[key].(map[string]any)
		if next == nil {
			next = make(map[string]any)
			current[key] = next
		}
		current = next
	}
	current[path[len(path)-1]] = value
}

func changedConfigPaths(before, after map[string]any, prefix string) []string {
	keys := make(map[string]struct{}, len(before)+len(after))
	for key := range before {
		keys[key] = struct{}{}
	}
	for key := range after {
		keys[key] = struct{}{}
	}
	out := make([]string, 0)
	for key := range keys {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		left, leftOK := before[key]
		right, rightOK := after[key]
		lm, lok := left.(map[string]any)
		rm, rok := right.(map[string]any)
		if lok && rok {
			out = append(out, changedConfigPaths(lm, rm, path)...)
		} else if leftOK != rightOK || !configValuesEqual(left, right) {
			out = append(out, path)
		}
	}
	return out
}

func configValuesEqual(left, right any) bool {
	leftValue, leftNumber := numericConfigValue(left)
	rightValue, rightNumber := numericConfigValue(right)
	if leftNumber || rightNumber {
		return leftNumber && rightNumber && leftValue == rightValue
	}
	return reflect.DeepEqual(left, right)
}

func numericConfigValue(value any) (float64, bool) {
	if value == nil {
		return 0, false
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(v.Uint()), true
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	default:
		return 0, false
	}
}
