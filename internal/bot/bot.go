package bot

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/internal/aichat"
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
	bot                      *tb.Bot
	cfg                      *config.Config
	musicDedupe              *musicdedupe.Store
	pendingMu                sync.Mutex
	pendingMusic             map[string]pendingMusicDownload
	activeMusicKeys          map[string]struct{}
	musicActionMu            sync.Mutex
	musicActions             map[string]pendingMusicAction
	playlistAssignmentMu     sync.Mutex
	playlistAssignments      map[string]pendingPlaylistAssignment
	playlistAssignmentBySong map[string]string
	aiStore                  *aichat.Store
	aiSkills                 *aichat.SkillRegistry
	aiMCP                    []*aichat.MCPClient
	aiMCPTools               []aichat.Tool
	aiTurnMu                 sync.Mutex
	aiTurnLocks              map[int64]*sync.Mutex
	aiBatchRunning           map[int64]struct{}
	aiPlaylistMu             sync.Mutex
	aiPlaylistRunning        map[int64]struct{}
	emptyTrashMu             sync.Mutex
	emptyTrashRunning        map[int64]struct{}
	emptyTrashPending        map[string]pendingEmptyTrash
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
		// Keep telebot's handler (it owns the update channel fields), but
		// isolate its stop channel: Webhook.Poll closes the channel it is
		// given, while Bot.Start closes the same channel again on shutdown.
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
		bot:                      bot,
		cfg:                      cfg,
		musicDedupe:              musicDedupe,
		pendingMusic:             make(map[string]pendingMusicDownload),
		activeMusicKeys:          make(map[string]struct{}),
		musicActions:             make(map[string]pendingMusicAction),
		playlistAssignments:      make(map[string]pendingPlaylistAssignment),
		playlistAssignmentBySong: make(map[string]string),
		aiTurnLocks:              make(map[int64]*sync.Mutex),
		aiBatchRunning:           make(map[int64]struct{}),
		aiPlaylistRunning:        make(map[int64]struct{}),
		emptyTrashRunning:        make(map[int64]struct{}),
		emptyTrashPending:        make(map[string]pendingEmptyTrash),
	}
	app.initAI()
	//注册处理器
	app.registerHandlers()
	// Publish the menu once at startup. Telegram stores it per bot, so doing it
	// here means every deploy picks up the new order and wording without the
	// user having to send /setCommands manually.
	go app.syncCommandMenu()
	//初始化notifier
	InitBotNotifier(bot, cfg.Telegram.ChatID, app)
	return app, nil
}

type webhookPoller struct {
	webhook *tb.Webhook
}

// Poll delegates webhook serving to telebot so its private dest/update fields
// are initialized, but gives it a child stop channel. The parent stop channel
// belongs to Bot.Start, which closes it after Poll returns; closing that same
// channel twice is what caused "close of closed channel".
func (p webhookPoller) Poll(b *tb.Bot, updates chan tb.Update, stop chan struct{}) {
	child := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		p.webhook.Poll(b, updates, child)
		close(done)
	}()
	select {
	case <-done:
	case <-stop:
		signalWebhookStop(child)
		<-done
	}
}

// signalWebhookStop unblocks telebot's waitForStop with a value, not a close.
// If Poll has already closed the child channel (for example SetWebhook failed),
// the send panics; recover keeps shutdown idempotent.
func signalWebhookStop(stop chan struct{}) {
	defer func() { _ = recover() }()
	stop <- struct{}{}
}

// Start 启动机器人
func (app *BotApp) Start() {
	// In webhook mode telebot registers the webhook as part of Poll; removing it
	// here would delete the endpoint we are about to serve. Polling mode still
	// needs the removal so a stale webhook cannot swallow updates.
	if app.cfg.Telegram.Mode != 2 {
		_ = app.bot.RemoveWebhook(true)
	}
	app.bot.Start()
}

// syncCommandMenu publishes botCommands so the "/" menu always matches the
// handlers registered above. It is best-effort: a Telegram hiccup must not stop
// the bot from starting.
func (app *BotApp) syncCommandMenu() {
	if err := app.bot.SetCommands(botCommands()); err != nil {
		utils.WarnWithFormat("[Telegram] 同步命令菜单失败: %v", err)
		return
	}
	utils.InfoWithFormat("[Telegram] 命令菜单已同步（%d 条）", len(botCommands()))
}

// Stop 关闭机器人
func (app *BotApp) Stop() {
	app.bot.Stop()
	if app.musicDedupe != nil {
		_ = app.musicDedupe.Close()
	}
	if app.aiStore != nil {
		_ = app.aiStore.Close()
	}
}
