package config

import (
	"fmt"
	"os"
	"sync"

	"github.com/goccy/go-yaml"
)

var (
	configCache *Config
	cacheOnce   sync.Once
)

// LoadConfig 加载配置并填充默认值
func LoadConfig(file string) *Config {
	// 如果已经加载过，直接返回缓存
	cacheOnce.Do(func() {
		bytes, err := os.ReadFile(file)
		if err != nil {
			fmt.Println("⚙️ 配置文件未找到或读取失败:", err)
			c := createDefaultConfig()
			applyPlaylistSidecar(file, c)
			go saveDefaultConfig(file, c)
			configCache = c
			return
		}

		if len(bytes) == 0 {
			fmt.Println("⚠️ 配置文件为空，生成默认配置")
			c := createDefaultConfig()
			applyPlaylistSidecar(file, c)
			go saveDefaultConfig(file, c)
			configCache = c
			return
		}

		c := &Config{}
		if err := yaml.Unmarshal(bytes, c); err != nil {
			fmt.Println("⚠️ 配置文件解析失败:", err)
			go backupOldConfig(file, bytes)
			c = createDefaultConfig()
			applyPlaylistSidecar(file, c)
			go saveDefaultConfig(file, c)
			configCache = c
			return
		}

		c.setDefaults()
		applyPlaylistSidecar(file, c)
		configCache = c
	})

	return configCache
}

// applyPlaylistSidecar overlays config.playlists.yaml when it is present.
// A broken sidecar must not prevent startup: keep the inline list and surface a
// warning so the operator can repair it from the WebUI.
func applyPlaylistSidecar(file string, c *Config) {
	EnsurePlaylistFile(file)
	if err := ApplyPlaylistFile(c, file); err != nil {
		fmt.Println("⚠️ 歌单配置文件加载失败，继续使用 config.yaml 内联歌单:", err)
	}
}

// 创建默认配置
func createDefaultConfig() *Config {
	c := &Config{}
	c.setDefaults()
	return c
}

// 保存默认配置
func saveDefaultConfig(file string, c *Config) {
	data, err := yaml.Marshal(c)
	if err != nil {
		fmt.Println("❌ 序列化默认配置失败:", err)
		return
	}
	dir := getDir(file)
	if dir != "" {
		_ = os.MkdirAll(dir, 0755)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		fmt.Println("❌ 写入默认配置失败:", err)
	} else {
		fmt.Println("✅ 默认配置文件已创建:", file)
	}
}

// 备份旧配置（异步调用）
func backupOldConfig(file string, data []byte) {
	backupFile := file + ".bak"
	if err := os.WriteFile(backupFile, data, 0644); err != nil {
		fmt.Println("⚠️ 备份旧配置失败:", err)
	} else {
		fmt.Println("🗂️ 已备份旧配置为:", backupFile)
	}
}

// 获取目录路径
func getDir(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[:i]
		}
	}
	return ""
}

// 填充默认值
func (c *Config) setDefaults() {
	if c.WebConfig == nil {
		c.WebConfig = &WebConfig{
			Enable:    false,
			AppDomain: "localhost",
			Https:     false,
			AppPort:   8080,
			GinMode:   "debug",
			Auth:      WebAuthConfig{},
		}
	}
	if c.CookieCloud == nil {
		c.CookieCloud = &CookieCloudConfig{
			Mode:            1,
			CookieCloudUrl:  "",
			CookieCloudUUID: "",
			CookieCloudKEY:  "",
			CookieFile:      "cookies.txt",
			CookieFilePath:  "data/temp",
			ExpireTime:      180,
		}
	}
	if c.Tidy == nil {
		c.Tidy = &TidyConfig{
			Mode:    1,
			DistDir: "data/dist",
		}
	}
	if c.WebDAV == nil {
		c.WebDAV = &WebDAVConfig{
			WebDAVUrl:  "",
			WebDAVUser: "",
			WebDAVPass: "",
			WebDAVDir:  "",
			WebDavHost: "",
		}
	}
	if c.Log == nil {
		c.Log = &LogConfig{
			Mode:  1,
			Level: 2,
			File:  "data/logs/run.log",
		}
	}
	if c.Telegram == nil {
		c.Telegram = &TelegramConfig{
			Enable: false,
			Mode:   1,
		}
	}
	if c.QQMusicApiConfig == nil {
		c.QQMusicApiConfig = &QQMusicApiConfig{
			Enable: false,
		}
	}
	if c.AI == nil {
		c.AI = &AIConfig{
			Enable:                false,
			BaseUrl:               "https://api.openai.com/v1",
			Model:                 "gpt-3.5-turbo",
			DefaultMode:           false,
			MaxToolRounds:         4,
			RequestTimeoutSeconds: 90,
			HistoryWindowDays:     30,
			MaxSuggestions:        10,
			SkillsDirs:            []string{"skills"},
			EnabledSkills:         defaultMusicSkills(),
			MCPServers:            make([]MCPServerConfig, 0),
			HistoryTable:          "music_scrobble_records",
		}
	} else {
		if len(c.AI.SkillsDirs) == 0 {
			c.AI.SkillsDirs = []string{"skills"}
		}
		if len(c.AI.EnabledSkills) == 0 {
			c.AI.EnabledSkills = defaultMusicSkills()
		}
		if c.AI.MaxToolRounds <= 0 {
			c.AI.MaxToolRounds = 4
		}
		if c.AI.RequestTimeoutSeconds <= 0 {
			c.AI.RequestTimeoutSeconds = 90
		}
		if c.AI.HistoryWindowDays <= 0 {
			c.AI.HistoryWindowDays = 30
		}
		if c.AI.MaxSuggestions <= 0 {
			c.AI.MaxSuggestions = 10
		}
		if c.AI.HistoryTable == "" {
			c.AI.HistoryTable = "music_scrobble_records"
		}
		for i := range c.AI.MCPServers {
			if c.AI.MCPServers[i].TimeoutSeconds <= 0 {
				c.AI.MCPServers[i].TimeoutSeconds = 20
			}
		}
	}
	if c.LrcAPI == nil {
		c.LrcAPI = &LrcAPIConfig{
			Enable:     false,
			LrcApiUrl:  "",
			LrcApiKey:  "",
			LrcApiHost: "",
		}
	}
	if c.N8NConfig == nil {
		c.N8NConfig = &N8NConfig{
			Enable:               false,
			N8NBaseUrl:           "",
			AuthToken:            "",
			EmptyTrashEndpoint:   "",
			AIPlaylistEndpoint:   "",
			TidyPlaylistEndpoint: "",
			PlaylistAssist:       false,
			TidyPlaylist:         make([]PlaylistInfo, 0),
		}
	}
	if c.AdditionalConfig == nil {
		c.AdditionalConfig = &AdditionalConfig{
			EnableCron:       false,
			EnableDirMonitor: false,
			MonitorDirs:      make([]string, 0),
			MusicMode:        false,
		}
	}
	if c.ProxyConfig == nil {
		c.ProxyConfig = &ProxyConfig{
			Enable: false,
		}
	}
}

// defaultMusicSkills is the Telegram-facing allow-list. Deployment and
// host-control skills are deliberately excluded.
func defaultMusicSkills() []string {
	return []string{
		"music-recommendation",
		"music-library-query",
		"music-download-failure",
		"music-playlist-curation",
		"music-discovery-by-artist",
		"music-lyrics-search",
		"music-metadata-normalize",
		"music-download-plan",
	}
}

// SetDefaultsForTest fills defaults without touching the loader cache. It is
// used by tests that assert config defaults stay in sync with shipped skills.
func (c *Config) SetDefaultsForTest() {
	c.setDefaults()
}
