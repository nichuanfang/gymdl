package bot

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
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
	msg := `📖 <b>怎么用</b>

1. 发歌曲链接 → 直接下载入库
2. 发 /ai 加一句话 → 找歌、推荐、查曲库
3. /mode → 切换后可直接说话

<b>常用</b>
/mode 切换模式
/clear 清空对话
/help 看这条说明

<b>歌单</b>
/assign_playlist 按标签整理
/ai_playlist 生成新歌单
/empty_trash 清空回收站`

	if err := c.Send(msg, tb.ModeHTML); err != nil {
		logger.Error("Failed to send help message", zap.Error(err))
		return err
	}
	return nil
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

	if !app.cfg.QQMusicApiConfig.Enable {
		if err := c.Send("QQ服务暂时不可用"); err != nil {
			logger.Error("Failed to send message", zap.Error(err))
			return err
		}
		return nil
	}

	wrapper := NewQQMusicWrapper(app.cfg.QQMusicApiConfig)
	b64_data, identifier, err := wrapper.fetchQRCode()
	if err != nil {
		logger.Error("Failed to fetch QR Code", zap.Error(err))
		return err
	}
	imgData, err := base64.StdEncoding.DecodeString(b64_data)
	if err != nil {
		return c.Send("解析二维码失败")
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
	var res utils.BaseResponse[map[string]interface{}]
	if err := json.Unmarshal(data, &res); err != nil {
		return "", "", err
	}

	return res.Data["data"].(string), res.Data["identifier"].(string), nil
}

// 检查二维码登录状态
func (wrapper *QQMusicWrapper) checkQRCode(identifier string) (float64, interface{}, error) {
	data, err := wrapper.client.Request(http.MethodGet, fmt.Sprintf("/login/qrcode/%s/status", wrapper.loginType), map[string]string{"identifier": identifier}, nil)
	if err != nil {
		return -1, nil, err
	}

	// 解析业务特定的 JSON
	var res utils.BaseResponse[map[string]interface{}]
	if err := json.Unmarshal(data, &res); err != nil {
		return -1, nil, err
	}

	return res.Data["event"].(float64), res.Data["credential"], nil
}

// 异步轮询
func (wrapper *QQMusicWrapper) pollLoginStatus(msg *tb.Message, identifier string) {
	ticker := time.NewTicker(3 * time.Second) // 3秒轮询一次
	defer ticker.Stop()

	timeout := time.After(2 * time.Minute) // 2分钟超时

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
				_, _ = app.bot.EditCaption(msg, "✅已扫码,请在手机上确认")
			case 0: // DONE
				_, _ = app.bot.EditCaption(msg, "🎉 登录成功！")
				cred := credential.(map[string]interface{})
				expiredAtUnix := int64(cred["expired_at"].(float64))
				authText := fmt.Sprintf(
					"请复制以下凭证并妥善保存：\n\n"+
						"🆔 *Music ID*\n`%.0f`\n\n"+
						"🏷️ *Str Music ID*\n`%s`\n\n"+
						"👤 *OpenID*\n`%s`\n\n"+
						"🌐 *UnionID*\n`%s`\n\n"+
						"🎫 *Login Type*\n`%.0f`\n\n"+
						"🔑 *Music Key*\n`%s`\n\n"+
						"🔄 *Refresh Key*\n`%s`\n\n"+
						"⏳ *Refresh Token*\n`%s`\n\n"+
						"🚀 *Access Token*\n`%s`\n\n"+
						"📅 *Expired At*\n`%d`\n\n"+
						"⚠️ *注意：凭证信息请勿泄露给他人。*",
					cred["musicid"].(float64),
					cred["str_musicid"].(string),
					cred["openid"].(string),
					cred["unionid"].(string),
					cred["loginType"].(float64),
					cred["musickey"].(string),
					cred["refresh_key"].(string),
					cred["refresh_token"].(string),
					cred["access_token"].(string),
					expiredAtUnix,
				)

				// 3. 推送给用户
				_, _ = app.bot.Send(msg.Chat, authText, &tb.SendOptions{ParseMode: tb.ModeMarkdown})
				return
			case 3: // TIMEOUT
				_, _ = app.bot.EditCaption(msg, "❌ 二维码已过期，请重新发起登录")
				// 建议：此处可以删除过期图片
				_ = app.bot.Delete(msg)
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

/* ------------------------ 初始化指令集 ------------------------ */

// botCommands 是 / 菜单的唯一来源。顺序按手机上的使用频率排，描述控制在
// 4-5 个汉字，避免在窄屏上换行或被截断；不加 emoji 和多余空格。
// /start 不放进菜单（少数 7 条更好点），新用户点 Start 按钮仍会触发它。
func botCommands() []tb.Command {
	return []tb.Command{
		{Text: "ai", Description: "对话找歌"},
		{Text: "mode", Description: "切换模式"},
		{Text: "clear", Description: "清空对话"},
		{Text: "assign_playlist", Description: "整理歌单"},
		{Text: "ai_playlist", Description: "生成歌单"},
		{Text: "empty_trash", Description: "清空回收站"},
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
