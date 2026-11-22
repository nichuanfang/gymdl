package bot

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/utils"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v4"
)

/* ------------------------ /start ------------------------ */

// StartCommand 响应 /start 命令
func StartCommand(c tb.Context) error {
	msg := `👋 欢迎使用 GymDL

发链接：直接下载
发问题：/ai 帮你找歌
/mode 切换 AI 与普通模式

完整说明：/help`

	if err := c.Send(msg); err != nil {
		logger.Error("Failed to send start message", zap.Error(err))
		return err
	}
	return nil
}

/* ------------------------ /help ------------------------ */

// HelpCommand 给一份手机上能一屏看完的用法说明。命令菜单本身已经列出全部
// 指令，这里只解释最常用的三条路径，不再重复堆一遍命令表。
func HelpCommand(c tb.Context) error {
	if err := c.Send(helpText(), tb.ModeHTML); err != nil {
		logger.Error("Failed to send help message", zap.Error(err))
		return err
	}
	return nil
}

func helpText() string {
	return `📖 <b>GymDL 使用指南</b>

<b>下载</b>
发送歌曲链接或音频文件，机器人会自动整理入库。完成后可试听或删除本地文件。

<b>AI 助手</b>
/ai <code>需求</code> 单次提问、找歌或查曲库
/mode 切换 AI 模式；链接始终优先下载
/clear 清空当前对话，不会切换模式

<b>歌单</b>
/assign_playlist 整理尚未归档的歌曲
/playlist_desc 同步歌单并维护整理说明
/ai_playlist 预览后创建或更新 AI 歌单
/rediscover 从历史和收藏找曲库缺少的歌曲
/recommend 个性化推荐，可加曲风或场景，如 /recommend 粤语爵士
/favorites 管理账号、同步收藏并查看入库队列
/audio_check 检查低码率、损坏和疑似缺尾的歌曲
/empty_trash 预览后清空回收站
删除歌曲文件，并清理 MusicRAG 状态与 Pinecone 向量

<b>账号</b>
/qq_login 扫码登录 QQ 音乐（仅限私聊）
/status 查看下载、收藏入库、最近失败、待入库和 Cookie`
}

/* ------------------------ /qq_login ------------------------ */

// 包装器
type QQMusicWrapper struct {
	client    *utils.CommonClient
	config    *config.QQMusicApiConfig
	loginType string
}

// 带特殊配置的客户端 还可以初始化http客户端
func NewQQMusicWrapper(config *config.QQMusicApiConfig) *QQMusicWrapper {
	if config == nil {
		return nil
	}
	c := utils.NewCommonClient(config.Endpoint, 60*time.Second)

	// 注入业务特定的 Header
	c.SetHeader("Referer", "https://y.qq.com/")
	c.SetHeader("User-Agent", "Mozilla/5.0...")

	var login_type string
	switch config.LoginType {
	case 0:
		login_type = "qq"
	case 1:
		login_type = "wx"
	default:
		login_type = "mobile"
	}

	return &QQMusicWrapper{
		client:    c,
		config:    config,
		loginType: login_type,
	}
}

// 处理QQ音乐登录
func (app *BotApp) QQLoginCommand(c tb.Context) error {
	// 1. 调用接口`/login/get_qrcode`发送二维码.收到二维码后回复并展示二维码图片给用户,提示用户扫码
	// 2. 轮询调用`/login/check_qrcode`判断是否已登录,可能收到的状态为: SCAN(待扫码) DONE(已登录) TIMEOUT(二维码已过期) REFUSE(拒绝) OTHER(其他情况)
	// 3. 根据上一步调用的结果做不同的处理

	// tips:
	//    a. 调用外系统接口 最好可配置,高性能
	//    b. 二维码过期需要删除旧二维码
	//    c. 代码实现以用户体验优先

	if c.Chat() == nil || c.Chat().Type != tb.ChatPrivate {
		return c.Send("为保护登录凭证，请在与机器人的私聊中使用 /qq_login。")
	}
	if app.cfg == nil || app.cfg.QQMusicApiConfig == nil || !app.cfg.QQMusicApiConfig.Enable {
		if err := c.Send("QQ服务暂时不可用"); err != nil {
			logger.Error("Failed to send message", zap.Error(err))
			return err
		}
		return nil
	}

	wrapper := NewQQMusicWrapper(app.cfg.QQMusicApiConfig)
	if wrapper == nil {
		return c.Send("QQ 登录服务配置无效，请联系管理员。")
	}
	b64_data, identifier, err := wrapper.fetchQRCode()
	if err != nil {
		logger.Error("Failed to fetch QR Code", zap.Error(err))
		return c.Send("获取登录二维码失败，请稍后重试。")
	}
	imgData, err := base64.StdEncoding.DecodeString(b64_data)
	if err != nil || len(imgData) == 0 {
		return c.Send("解析登录二维码失败，请稍后重试。")
	}

	var platformName string
	if wrapper.loginType == "wx" {
		platformName = "微信"
	} else {
		platformName = "QQ"
	}

	// 封装成 telebot 可发送的对象
	photo := &tb.Photo{
		File:    tb.FromReader(bytes.NewReader(imgData)),
		Caption: fmt.Sprintf("请使用%s扫描上方二维码登录\n状态：⌛等待扫码...", platformName),
	}

	// 发送图片，并获取发送后的消息对象（用于后续更新状态或删除）
	sentMsg, err := app.bot.Send(c.Recipient(), photo)
	if err != nil {
		return err
	}

	// 开启异步轮询 (协程)
	go wrapper.pollLoginStatus(sentMsg, identifier)
	return nil

}

// 获取二维码
func (wrapper *QQMusicWrapper) fetchQRCode() (string, string, error) {
	data, err := wrapper.client.Request(http.MethodGet, "/login/qrcode/"+wrapper.loginType, nil, nil)
	if err != nil {
		return "", "", err
	}

	// 解析业务特定的 JSON
	var envelope struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return "", "", err
	}
	var image, identifier string
	if err := json.Unmarshal(envelope.Data["data"], &image); err != nil || strings.TrimSpace(image) == "" {
		return "", "", errors.New("QQ 登录服务未返回有效二维码")
	}
	if err := json.Unmarshal(envelope.Data["identifier"], &identifier); err != nil || strings.TrimSpace(identifier) == "" {
		return "", "", errors.New("QQ 登录服务未返回二维码标识")
	}
	return image, identifier, nil
}

// 检查二维码登录状态
func (wrapper *QQMusicWrapper) checkQRCode(identifier string) (int, json.RawMessage, error) {
	data, err := wrapper.client.Request(http.MethodGet, fmt.Sprintf("/login/qrcode/%s/status", wrapper.loginType), map[string]string{"identifier": identifier}, nil)
	if err != nil {
		return -1, nil, err
	}
	var envelope struct {
		Data struct {
			Event      int             `json:"event"`
			Credential json.RawMessage `json:"credential"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return -1, nil, err
	}
	return envelope.Data.Event, envelope.Data.Credential, nil
}

// 异步轮询
func (wrapper *QQMusicWrapper) pollLoginStatus(msg *tb.Message, identifier string) {
	ticker := time.NewTicker(3 * time.Second) // 3秒轮询一次
	defer ticker.Stop()

	timeout := time.After(2 * time.Minute) // 2分钟超时
	lastStatus := ""

	for {
		select {
		case <-ticker.C:
			status, credential, err := wrapper.checkQRCode(identifier)
			if err != nil {
				continue
			}

			switch status {
			case 1: // SCAN
				continue
			case 2: // CONF
				if lastStatus != "scanned" {
					_, _ = app.bot.EditCaption(msg, "✅ 已扫码，请在手机上确认登录")
					lastStatus = "scanned"
				}
			case 0: // DONE
				_, _ = app.bot.EditCaption(msg, "🎉 登录成功！")
				authText, formatErr := formatQQCredentialMessage(credential)
				if formatErr != nil {
					utils.WarnWithFormat("[Telegram] QQ 登录完成，但凭证响应格式无效: %v", formatErr)
					_, _ = app.bot.Send(msg.Chat, "QQ 登录已成功，但凭证数据格式异常，请重新发起登录。")
					return
				}
				if _, sendErr := app.bot.Send(msg.Chat, authText, &tb.SendOptions{ParseMode: tb.ModeHTML}); sendErr != nil {
					utils.WarnWithFormat("[Telegram] 发送 QQ 登录凭证失败: %v", sendErr)
					_, _ = app.bot.EditCaption(msg, "⚠️ 登录成功，但凭证消息发送失败，请重新发起登录。")
				}
				return
			case 3: // TIMEOUT
				_, _ = app.bot.EditCaption(msg, "❌ 二维码已过期，请重新发起登录")
				return
			case 4: // REFUSE
				_, _ = app.bot.EditCaption(msg, "🚫 您拒绝了登录申请")
				return
			}

		case <-timeout:
			app.bot.EditCaption(msg, "⌛️ 登录超时，请重试")
			return
		}
	}
}

func formatQQCredentialMessage(raw json.RawMessage) (string, error) {
	var credential map[string]json.RawMessage
	if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &credential) != nil || len(credential) == 0 {
		return "", errors.New("credential is missing or invalid")
	}
	fields := []struct {
		key   string
		label string
	}{
		{"musicid", "Music ID"},
		{"str_musicid", "Str Music ID"},
		{"openid", "OpenID"},
		{"unionid", "UnionID"},
		{"loginType", "Login Type"},
		{"musickey", "Music Key"},
		{"refresh_key", "Refresh Key"},
		{"refresh_token", "Refresh Token"},
		{"access_token", "Access Token"},
		{"expired_at", "Expired At"},
	}
	var text strings.Builder
	text.WriteString("请复制以下凭证并妥善保存：\n\n")
	for _, field := range fields {
		value, err := qqCredentialValue(credential[field.key])
		if err != nil {
			return "", fmt.Errorf("credential field %s is missing or invalid", field.key)
		}
		fmt.Fprintf(&text, "🧾 <b>%s</b>\n<code>%s</code>\n\n", field.label, utils.EscapeHTML(value))
	}
	text.WriteString("⚠️ 凭证具有账户访问权限，请勿转发或公开。")
	return text.String(), nil
}

func qqCredentialValue(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("missing")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		if strings.TrimSpace(value) == "" {
			return "", errors.New("empty")
		}
		return value, nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err == nil && number.String() != "" {
		return number.String(), nil
	}
	return "", errors.New("invalid")
}

/* ------------------------ 初始化指令集 ------------------------ */

// botCommands 是 / 菜单的唯一来源。按用户指定的常用顺序排列，
// 找歌与对话控制在前，其后是歌单、遗珠、同步及帮助。
// /start 由 Start 按钮进入；/new 是 /clear 别名。/qq_login 可手动调用，不放进菜单。/status 放在帮助前面。
func botCommands() []tb.Command {
	return []tb.Command{
		{Text: "ai", Description: "对话找歌"},
		{Text: "mode", Description: "切换模式"},
		{Text: "clear", Description: "清空对话"},
		{Text: "assign_playlist", Description: "整理歌单"},
		{Text: "playlist_desc", Description: "歌单描述"},
		{Text: "ai_playlist", Description: "生成歌单"},
		{Text: "rediscover", Description: "找回遗珠"},
		{Text: "recommend", Description: "个性化推荐"},
		{Text: "favorites", Description: "收藏同步"},
		{Text: "audio_check", Description: "曲库体检"},
		{Text: "empty_trash", Description: "清空回收站"},
		{Text: "status", Description: "服务状态"},
		{Text: "help", Description: "使用帮助"},
	}
}

// SetCommands 初始化 Telegram 命令列表
func SetCommands(c tb.Context) error {
	if err := c.Bot().SetCommands(botCommands()); err != nil {
		logger.Error("Failed to set commands", zap.Error(err))
		return err
	}

	const successMsg = "✅ 命令菜单已更新"
	if err := c.Send(successMsg); err != nil {
		logger.Error("Failed to send confirmation message", zap.Error(err))
		return err
	}

	return nil
}

/* ------------------------ /assign_playlist ------------------------ */
func (app *BotApp) AssignPlaylistCommand(c tb.Context) error {
	return app.startPlaylistAssignment(c)
}

/* ------------------------ /ai_playlist ------------------------ */
func (app *BotApp) AIPlaylistCommand(c tb.Context) error {
	return app.startAIPlaylist(c)
}

/* ------------------------ /empty_trash ------------------------ */
func (app *BotApp) EmptyTrashCommand(c tb.Context) error {
	return app.startEmptyTrash(c)
}

/* ------------------------ 辅助方法 ------------------------ */
