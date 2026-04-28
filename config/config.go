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
			go saveDefaultConfig(file, c)
			configCache = c
			return
		}

		if len(bytes) == 0 {
			fmt.Println("⚠️ 配置文件为空，生成默认配置")
			c := createDefaultConfig()
			go saveDefaultConfig(file, c)
			configCache = c
			return
		}

		c := &Config{}
		if err := yaml.Unmarshal(bytes, c); err != nil {
			fmt.Println("⚠️ 配置文件解析失败:", err)
			go backupOldConfig(file, bytes)
			c = createDefaultConfig()
			go saveDefaultConfig(file, c)
			configCache = c
			return
		}

		c.setDefaults()
		configCache = c
	})

	return configCache
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
			Enable:  false,
			BaseUrl: "https://api.openai.com/v1",
			Model:   "gpt-3.5-turbo",
		}
	}
	if c.AdditionalConfig == nil {
		c.AdditionalConfig = &AdditionalConfig{
			EnableCron:       false,
			EnableDirMonitor: false,
			MonitorDirs:      make([]string, 0),
            MusicMode: false,
		}
	}
	if c.ProxyConfig == nil {
		c.ProxyConfig = &ProxyConfig{
			Enable: false,
		}
	}
}
