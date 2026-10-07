package bot

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/musicdedupe"
	"github.com/nichuanfang/gymdl/utils"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v4"
)

var (
	logger *zap.Logger
	app    *BotApp
)

type BotApp struct {
	bot             *tb.Bot
	cfg             *config.Config
	musicDedupe     *musicdedupe.Store
	pendingMu       sync.Mutex
	pendingMusic    map[string]pendingMusicDownload
	activeMusicKeys map[string]struct{}
}

// NewBotApp 创建机器人
func NewBotApp(cfg *config.Config) (*BotApp, error) {
	logger = utils.Logger()
	botSettings := tb.Settings{
		Token: cfg.Telegram.BotToken,
		// 默认使用 polling，后面可切换
		Poller: &tb.LongPoller{Timeout: 10},
	}

	// 检查是否启用代理配置
	if cfg.ProxyConfig.Enable {
		// 验证代理配置是否完整（Scheme、Host、Port 必须非空）
		if cfg.ProxyConfig.Scheme != "" && cfg.ProxyConfig.Host != "" && cfg.ProxyConfig.Port != 0 {
			// 初始化代理认证信息
			var userinfo *url.Userinfo
			if cfg.ProxyConfig.Auth {
				// 如果启用认证，设置用户名和密码
				userinfo = url.UserPassword(cfg.ProxyConfig.User, cfg.ProxyConfig.Pass)
			}
			// 配置 HTTP 客户端，使用代理
			botSettings.Client = &http.Client{
				Transport: &http.Transport{
					Proxy: http.ProxyURL(&url.URL{
						Scheme: cfg.ProxyConfig.Scheme,
						Host:   fmt.Sprintf("%s:%d", cfg.ProxyConfig.Host, cfg.ProxyConfig.Port),
						User:   userinfo,
					}),
				},
			}
		} else {
			// 代理配置不完整，记录日志
			utils.Logger().Info("ProxyConfig 配置不完整，未成功使用代理")
		}
	}

	if cfg.Telegram.Mode == 2 {
		botSettings.Poller = &tb.Webhook{
			Listen: ":" + fmt.Sprint(cfg.Telegram.WebhookPort),
			Endpoint: &tb.WebhookEndpoint{
				PublicURL: cfg.Telegram.WebhookURL,
			},
		}
	}

	bot, err := tb.NewBot(botSettings)
	if err != nil {
		return nil, err
	}
	musicDedupe, err := musicdedupe.Open(musicdedupe.DefaultDatabasePath)
	if err != nil {
		return nil, err
	}

	app = &BotApp{
		bot:             bot,
		cfg:             cfg,
		musicDedupe:     musicDedupe,
		pendingMusic:    make(map[string]pendingMusicDownload),
		activeMusicKeys: make(map[string]struct{}),
	}
	//注册处理器
	app.registerHandlers()
	//初始化notifier
	InitBotNotifier(bot, cfg.Telegram.ChatID)
	return app, nil
}

// Start 启动机器人
func (app *BotApp) Start() {
	//移除webhook
	_ = app.bot.RemoveWebhook(true)
	app.bot.Start()
}

// Stop 关闭机器人
func (app *BotApp) Stop() {
	app.bot.Stop()
	if app.musicDedupe != nil {
		_ = app.musicDedupe.Close()
	}
}
