package bot

import (
    "bytes"
    "encoding/base64"
    "encoding/json"
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
	msg := `👋 欢迎来到 GymDL Bot!
我可以帮你分析并下载多种音乐链接 🏋️‍♂️

使用 /help 查看可用命令 📜`

	if err := c.Send(msg, &tb.SendOptions{ParseMode: tb.ModeMarkdown}); err != nil {
		logger.Error("Failed to send start message", zap.Error(err))
		return err
	}
	return nil
}

/* ------------------------ /help ------------------------ */
// HelpCommand 响应 /help 命令，自动生成已注册命令说明
func HelpCommand(c tb.Context) error {
	commands, err := c.Bot().Commands()
	if err != nil {
		logger.Error("Failed to get bot commands", zap.Error(err))
		return c.Send("⚠️ 无法获取命令列表")
	}

	if len(commands) == 0 {
		return c.Send("😅 当前没有可用命令")
	}

	helpMsg := "📖 *可用命令列表:*\n\n"
	for _, cmd := range commands {
		helpMsg += fmt.Sprintf("• `/%s` - %s\n", cmd.Text, cmd.Description)
	}

	helpMsg += "\n✨ *Tip:* 你可以直接在聊天中输入命令来体验功能!"

	if err := c.Send(helpMsg, &tb.SendOptions{ParseMode: tb.ModeMarkdown}); err != nil {
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
	if config.EnableSign {
		c.SetHeader("X-Enable-Sign", "true")
	}

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
	data, err := wrapper.client.Request(http.MethodGet, "/login/get_qrcode", map[string]string{"login_type": wrapper.loginType}, nil)
	if err != nil {
		return "", "", err
	}

	// 解析业务特定的 JSON
	var res utils.BaseResponse[map[string]interface{}]
	if err := json.Unmarshal(data, &res); err != nil {
		return "", "", err
	}

	return res.Data["b64_data"].(string), res.Data["identifier"].(string), nil
}

// 检查二维码登录状态
func (wrapper *QQMusicWrapper) checkQRCode(identifier string) (string, interface{}, error) {
	data, err := wrapper.client.Request(http.MethodGet, "/login/check_qrcode", map[string]string{"qr_type": wrapper.loginType, "identifier": identifier}, nil)
	if err != nil {
		return "", nil, err
	}

	// 解析业务特定的 JSON
	var res utils.BaseResponse[map[string]interface{}]
	if err := json.Unmarshal(data, &res); err != nil {
		return "", nil, err
	}

	return res.Data["event"].(string), res.Data["credential"], nil
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
			case "SCAN":
				continue
			case "CONF":
				app.bot.EditCaption(msg, "✅已扫码,请在手机上确认")
			case "DONE":
				app.bot.EditCaption(msg, "🎉 登录成功！")
				cred := credential.(map[string]interface{})
				expiredAtUnix := int64(cred["expired_at"].(float64))
				expiredTime := time.Unix(expiredAtUnix, 0).Format("2006-01-02 15:04:05")
				authText := fmt.Sprintf(
					"请复制以下凭证并妥善保存：\n\n"+
						"🆔 *Music ID*\n`%.0f`\n\n"+
						"👤 *OpenID*\n`%s`\n\n"+
						"🌐 *UnionID*\n`%s`\n\n"+
						"🎫 *Login Type*\n`%.0f`\n\n"+
						"🔑 *Music Key*\n`%s`\n\n"+
						"🔄 *Refresh Key*\n`%s`\n\n"+
						"⏳ *Refresh Token*\n`%s`\n\n"+
						"🚀 *Access Token*\n`%s`\n\n"+
						"📅 *Expired At*\n`%s`\n\n"+
						"⚠️ *注意：凭证信息请勿泄露给他人。*",
					cred["musicid"].(float64),
					cred["openid"].(string),
					cred["unionid"].(string),
					cred["login_type"].(float64),
					cred["musickey"].(string),
					cred["refresh_key"].(string),
					cred["refresh_token"].(string),
					cred["access_token"].(string),
					expiredTime,
				)

				// 3. 推送给用户
				app.bot.Send(msg.Chat, authText, &tb.SendOptions{ParseMode: tb.ModeMarkdown})
				return
			case "TIMEOUT":
				app.bot.EditCaption(msg, "❌ 二维码已过期，请重新发起登录")
				// 建议：此处可以删除过期图片
				app.bot.Delete(msg)
				return
			case "REFUSE":
				app.bot.EditCaption(msg, "🚫 您拒绝了登录申请")
				return
			}

		case <-timeout:
			app.bot.EditCaption(msg, "⌛️ 登录超时，请重试")
			return
		}
	}
}

/* ------------------------ 初始化指令集 ------------------------ */
// SetCommands 初始化 Telegram 命令列表
func SetCommands(c tb.Context) error {
	commands := []tb.Command{
		{Text: "start", Description: "启动 Bot 👋"},
		{Text: "help", Description: "获取帮助 ❔"},
		// {Text: "qq_login", Description: "QQ 音乐登录 🎧"},
		{Text: "assign_playlist", Description: "分配歌单 🎵"},
	}

	if err := c.Bot().SetCommands(commands); err != nil {
		logger.Error("Failed to set commands", zap.Error(err))
		return err
	}

	const successMsg = "✅ 指令初始化成功，使用 /help 查看可用命令"
	if err := c.Send(successMsg); err != nil {
		logger.Error("Failed to send confirmation message", zap.Error(err))
		return err
	}

	return nil
}

/* ------------------------ /assign_playlist ------------------------ */
func (app *BotApp) AssignPlaylistCommand(c tb.Context) error {
    if len(app.cfg.N8NConfig.N8NBaseUrl) == 0 {
        _ = c.Send("请先配置 n8n_config.n8n_base_url")
        return nil
    }
    
    // 1. 先发一条初始消息，并拿到消息对象（用于 n8n 后续 Edit）
    sentMsg, err := c.Bot().Send(c.Chat(), "🚀 任务已提交，后台处理中...")
    if err != nil {
        return err
    }

    // 2. 拼接完整的 Webhook URL
    baseUrl, _ := strings.CutSuffix(app.cfg.N8NConfig.N8NBaseUrl, "/")
    endpoint, _ := strings.CutPrefix(app.cfg.N8NConfig.TidyPlaylistEndpoint, "/")
    targetUrl := baseUrl + "/" + endpoint
    
    // 3. 是否开启歌单分类ai增强
    playlistAssist := false
    if app.cfg.AI.Enable { 
        playlistAssist = app.cfg.AI.PlaylistAssist
    }
    // 4. 发送给 n8n 的数据
    payload := map[string]interface{}{
        "chat_id":    c.Chat().ID,
        "message_id": sentMsg.ID, 
        "username":   c.Sender().Username,
        "text":       c.Text(),
        "timestamp":  time.Now().Unix(),
        "playlist_assist": playlistAssist, // 是否开启歌单分类ai增强
        "tidy_playlist": app.cfg.N8NConfig.TidyPlaylist   , // 你的歌单列表 
    }

    // 5. 将数据序列化为 JSON 字节数组
    jsonData, err := json.Marshal(payload)
    if err != nil {
        _, _ = c.Bot().Edit(sentMsg, "❌ 数据解析失败: "+err.Error())
        return err
    }

    // 6. 创建 POST 请求
    req, err := http.NewRequest("POST", targetUrl, bytes.NewBuffer(jsonData))
    if err != nil {
        _, _ = c.Bot().Edit(sentMsg, "❌ 创建请求失败: "+err.Error())
        return err
    }
    req.Header.Set("Content-Type", "application/json")

    // 7. 同步发起请求
    // 因为 n8n 已经设置为立即响应，这里的 client.Do 会瞬间完成，不会卡住机器人
    client := &http.Client{
        Timeout: 5 * time.Second, // 5秒超时足够了，因为 n8n 不需要等待工作流执行完
    }
    resp, err := client.Do(req)
    if err != nil {
        _, _ = c.Bot().Edit(sentMsg, "❌ 调用 n8n 失败: "+err.Error())
        return err
    }
    defer resp.Body.Close()

    // 8. 校验 n8n 的接收状态
    if resp.StatusCode >= 200 && resp.StatusCode < 300 {
        // 💡 保持原样即可，不需要再发新消息，因为用户已经看到“🚀 任务已提交...”了
        // 接下来就静静等待 n8n 真正执行完工作流后来 Edit 这条消息
    } else {
        _, _ = c.Bot().Edit(sentMsg, fmt.Sprintf("❌ n8n 接收失败，状态码: %d", resp.StatusCode))
    }

    return nil
}
