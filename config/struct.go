package config

type Config struct {
	WebConfig        *WebConfig         `yaml:"web_config"`        // web配置
	CookieCloud      *CookieCloudConfig `yaml:"cookie_cloud"`      // cookiecloud配置
	Tidy             *TidyConfig        `yaml:"tidy"`              // 资源整理配置
	WebDAV           *WebDAVConfig      `yaml:"webdav"`            // webdav配置
	Log              *LogConfig         `yaml:"log"`               // 日志配置
	Telegram         *TelegramConfig    `yaml:"telegram"`          // telegram配置
	AI               *AIConfig          `yaml:"ai"`                // AI配置
	QQMusicApiConfig *QQMusicApiConfig  `yaml:"qq_music_api"`      // qq-music-api服务配置
	AdditionalConfig *AdditionalConfig  `yaml:"additional_config"` // 附属配置
	YTDLPConfig      *YTDLPConfig       `yaml:"ytdlp"`             //ytdlp配置
	ProxyConfig      *ProxyConfig       `yaml:"proxy"`             // 代理配置
}

type WebConfig struct {
	Enable    bool   `yaml:"enable"`     // 是否启用该服务
	AppDomain string `yaml:"app_domain"` // web服务domain
	Https     bool   `yaml:"https"`      // 是否开启了 https
	AppPort   int    `yaml:"app_port"`   // web服务监听端口
	GinMode   string `yaml:"gin_mode"`   // Gin的运行模式: 可选项[debug release test]
}

type CookieCloudConfig struct {
	Mode            int    `yaml:"mode"`             // cookiecloud同步模式: 1定时刷新 2webhook 默认为1 当同步模式为2时必须启用Gin Web服务
	CookieCloudUrl  string `yaml:"cookiecloud_url"`  // cookiecloud 地址
	CookieCloudUUID string `yaml:"cookiecloud_uuid"` // cookiecloud uuid
	CookieCloudKEY  string `yaml:"cookiecloud_key"`  // cookiecloud 密码（tips：如果有多个同步端需要上传需要绑定同一个key）
	CookieFile      string `yaml:"cookie_file"`      // cookie文件名
	CookieFilePath  string `yaml:"cookie_file_path"` // cookie存储目录
	ExpireTime      int    `yaml:"expire_time"`      // cookie文件过期时间(分钟) 根据自己需求控制
}

type TidyConfig struct {
	Mode    int    `yaml:"mode"`     // 资源整理模式: 1整理到DistDir, 2整理到webdav目录WebDAVDir
	DistDir string `yaml:"dist_dir"` // 整理到的路径,仅在Mode为1时使用该目录
}

type WebDAVConfig struct {
	WebDAVUrl  string `yaml:"webdav_url"`  // webdav地址
	WebDAVUser string `yaml:"webdav_user"` // webdav用户名
	WebDAVPass string `yaml:"webdav_pass"` // webdav密码
	WebDAVDir  string `yaml:"webdav_dir"`  // wevdav路径
}

type LogConfig struct {
	Mode  int    `yaml:"mode"`  // 日志模式：1标准输出，2日志文件，3标准输出和日志文件
	Level int    `yaml:"level"` // 日志等级，1=debug 2=info 3=warn 4=error 5=fatal
	File  string `yaml:"file"`  // 日志文件路径
}

type TelegramConfig struct {
	Enable       bool     `yaml:"enable"`        // 是否启用该服务
	Mode         int      `yaml:"mode"`          // 运行模式: 1长轮询, 2Webhook   开发环境推荐1,生产环境推荐2
	ChatID       string   `yaml:"chat_id"`       // chat_id 机器人ID
	BotToken     string   `yaml:"bot_token"`     // telegram机器人token
	AllowedUsers []string `yaml:"allowed_users"` // 白名单列表 填用户ID
	WebhookURL   string   `yaml:"webhook_url"`   // webhook地址 运行模式为2时必填
	WebhookPort  int      `yaml:"webhook_port"`  // webhook模式监听的端口
}

type AIConfig struct {
	Enable       bool   `yaml:"enable"`        // 是否开启AI
	BaseUrl      string `yaml:"base_url"`      // baseurl
	Model        string `yaml:"model"`         // 使用的模型
	ApiKey       string `yaml:"api_key"`       // apiKey
	SystemPrompt string `yaml:"system_prompt"` // 默认系统提示词
}

type QQMusicApiConfig struct {
	Enable       bool   `yaml:"enable"`        // 是否启用
	Endpoint     string `yaml:"endpoint"`      // qm-api服务地址
	LoginType    int    `yaml:"login_type"`    // qm-api登录方式: 0未登录 1微信 2QQ
	RefreshKey   string `yaml:"refresh_key"`   // 用于刷新已失效的musickey musickey1小时会过期
	RefreshToken string `yaml:"refresh_token"` //用于刷新已失效的musickey musickey1小时会过期
	MusicId      string `yaml:"music_id"`      //用于刷新已失效的musickey
	MusicKey     string `yaml:"music_key"`     //用于刷新已失效的musickey
	EnableSign   bool   `yaml:"enable_sign"`   // 是否启用加密接口 加密接口会多一个解密操作 但是更安全
	EnableCache  bool   `yaml:"enable_cache"`  // 是否启用接口缓存
}

type AdditionalConfig struct {
	EnableCron       bool     `yaml:"enable_cron"`    // 是否启用定时任务
	EnableDirMonitor bool     `yaml:"enable_monitor"` // 是否启用目录监听
	MonitorDirs      []string `yaml:"monitor_dirs"`   // 需要监听的目录  监听网易云/QQ下载目录=>调用um工具解锁=>整理=>telegram入库通知
}

type ProxyConfig struct {
	Enable bool   `yaml:"enable"` // 是否启用代理
	Scheme string `yaml:"scheme"` // 代理类型 http/socks5
	Host   string `yaml:"host"`   // 代理地址
	Port   int    `yaml:"port"`   // 代理端口
	User   string `yaml:"user"`   // 代理用户名
	Pass   string `yaml:"pass"`   // 代理密码
	Auth   bool   `yaml:"auth"`   // 是否需要认证
}

type YTDLPConfig struct {
	PassCookies      bool `yaml:"pass_cookies"`       //是否传递cookie  目前yt-dlp对传递cookie的支持不稳定 推荐设置为false
	YoutubeMusicMode bool `yaml:"youtube_music_mode"` //是否将youtube视频链接当作YoutubeMusic来处理
}
