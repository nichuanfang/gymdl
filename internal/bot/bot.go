package bot

import (
	"context"
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
	musicActionMu   sync.Mutex
	musicActions    map[string]pendingMusicAction
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
		webhook := &tb.Webhook{
			Listen: ":" + fmt.Sprint(cfg.Telegram.WebhookPort),
			Endpoint: &tb.WebhookEndpoint{
				PublicURL: cfg.Telegram.WebhookURL,
			},
		}
		botSettings.Poller = webhookPoller{webhook: webhook}
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
		musicActions:    make(map[string]pendingMusicAction),
	}
	//注册处理器
	app.registerHandlers()
	//初始化notifier
	InitBotNotifier(bot, cfg.Telegram.ChatID, app)
	return app, nil
}

type webhookPoller struct {
	webhook *tb.Webhook
}

// Poll owns the webhook HTTP server without calling Webhook.Poll: telebot
// v4's Webhook.waitForStop closes the stop channel itself, and Bot.Start
// closes the same channel afterwards, which panics during shutdown.
func (p webhookPoller) Poll(b *tb.Bot, updates chan tb.Update, stop chan struct{}) {
	if err := b.SetWebhook(p.webhook); err != nil {
		b.OnError(err, nil)
		return
	}
	server := &http.Server{
		Addr:    p.webhook.Listen,
		Handler: p.webhook,
	}
	go func() {
		<-stop
		_ = server.Shutdown(context.Background())
	}()
	if p.webhook.TLS != nil {
		_ = server.ListenAndServeTLS(p.webhook.TLS.Cert, p.webhook.TLS.Key)
		return
	}
	_ = server.ListenAndServe()
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
